import { WSManager } from './WSManager.js';
import { WSMessage, WSSignaling } from './WSTypes.js';
import { VideoPlayer } from './VideoPlayer.js';
import { Device } from './WSTypes.js';

export class MultiTrackRecorder {
    constructor() {
        this.tracks = new Map();
        this.nextTrackId = 1;
        this.wsManager = new WSManager();
        this.videoPlayer = new VideoPlayer();
        this.isRecording = false;
        this.sessionId = null;
        this.devices = {
            video: [],
            audio: []
        };
        this.previewSessions = new Map(); // 存储预览会话

        this.elements = {
            tracksContainer: document.getElementById('tracks-container'),
            addTrackButton: document.getElementById('addTrack'),
            startButton: document.getElementById('startRecording'),
            stopButton: document.getElementById('stopRecording'),
            status: document.getElementById('status')
        };

        // 检查必要的元素是否存在
        const missingElements = Object.entries(this.elements)
            .filter(([key, element]) => !element)
            .map(([key]) => key);

        if (missingElements.length > 0) {
            console.error('找不到必要的DOM元素:', missingElements);
            throw new Error(`找不到必要的DOM元素: ${missingElements.join(', ')}`);
        }

        this.init();
    }

    async init() {
        this.bindEvents();
        await this.connectWebSocket();
        await this.addTrack(); // 默认添加一个轨道
    }

    bindEvents() {
        this.elements.addTrackButton.addEventListener('click', () => this.addTrack());
        this.elements.startButton.addEventListener('click', () => this.startRecording());
        this.elements.stopButton.addEventListener('click', () => this.stopRecording());

        // WebSocket事件处理
        this.wsManager.on('devices', devices => this.handleDevicesList(devices));
        this.wsManager.on('recording_started', data => this.handleRecordingStarted(data));
        this.wsManager.on('recording_stopped', () => this.handleRecordingStopped());
        this.wsManager.on('error', error => this.handleError(error));
        this.wsManager.on('preview_started', data => this.handlePreviewStarted(data));
        this.wsManager.on('preview_stopped', data => this.handlePreviewStopped(data));
        this.wsManager.on('stream_data', data => {
            console.log('收到流数据:', data);
            this.videoPlayer.handleStreamData(data);
        });
    }

    async connectWebSocket() {
        try {
            await this.wsManager.connect();
            this.updateStatus('WebSocket已连接', 'ready');
        } catch (error) {
            this.updateStatus('WebSocket连接失败: ' + error.message, 'error');
        }
    }

    handleDevicesList(devices) {
        // 确保devices是数组
        if (!Array.isArray(devices)) {
            console.error('设备列表格式错误:', devices);
            return;
        }

        // 分类设备，保持设备原始类型
        this.devices = {
            video: devices.filter(device => device.type === 'camera' || device.type === 'screen'),
            audio: devices.filter(device => device.type === 'microphone' || device.type === 'speaker')
        };

        console.log('设备列表已更新:', this.devices);

        // 更新所有轨道的设备选项
        for (const [trackId] of this.tracks) {
            this.updateDeviceOptions(trackId);
        }
    }

    createTrackElement(trackId) {
        const trackElement = document.createElement('div');
        trackElement.id = `track-${trackId}`;
        trackElement.className = 'track-container';

        // 创建轨道头部
        const trackHeader = document.createElement('div');
        trackHeader.className = 'track-header';

        // 创建设备选择器容器
        const deviceSelectors = document.createElement('div');
        deviceSelectors.className = 'device-selectors';

        // 创建视频设备选择器
        const videoSelect = document.createElement('select');
        videoSelect.className = 'video-device';
        videoSelect.innerHTML = '<option value="">请选择视频设备</option>';

        // 创建音频设备选择器
        const audioSelect = document.createElement('select');
        audioSelect.className = 'audio-device';
        audioSelect.innerHTML = '<option value="">请选择音频设备</option>';

        // 创建视频格式选择器
        const formatSelect = document.createElement('select');
        formatSelect.className = 'video-format';
        formatSelect.innerHTML = '<option value="">请选择视频格式</option>';

        // 创建按钮容器
        const buttonContainer = document.createElement('div');
        buttonContainer.className = 'button-container';

        // 创建视频预览按钮
        const videoPreviewButton = document.createElement('button');
        videoPreviewButton.className = 'preview-button video-preview';
        videoPreviewButton.textContent = '视频预览';
        videoPreviewButton.disabled = true;

        // 创建音频预览按钮
        const audioPreviewButton = document.createElement('button');
        audioPreviewButton.className = 'preview-button audio-preview';
        audioPreviewButton.textContent = '音频预览';
        audioPreviewButton.disabled = true;

        // 创建删除按钮
        const removeButton = document.createElement('button');
        removeButton.className = 'remove-track';
        removeButton.textContent = '删除轨道';

        // 创建预览容器
        const previewContainer = document.createElement('div');
        previewContainer.className = 'preview-container';
        previewContainer.id = `preview-${trackId}`;

        // 组装设备选择器
        deviceSelectors.appendChild(videoSelect);
        deviceSelectors.appendChild(audioSelect);
        deviceSelectors.appendChild(formatSelect);
        buttonContainer.appendChild(videoPreviewButton);
        buttonContainer.appendChild(audioPreviewButton);
        buttonContainer.appendChild(removeButton);
        deviceSelectors.appendChild(buttonContainer);

        // 组装轨道头部
        trackHeader.appendChild(deviceSelectors);

        // 组装轨道元素
        trackElement.appendChild(trackHeader);
        trackElement.appendChild(previewContainer);

        // 添加到DOM
        this.elements.tracksContainer.appendChild(trackElement);

        // 绑定事件
        videoSelect.addEventListener('change', (e) => this.handleVideoDeviceChange(trackId, e));
        audioSelect.addEventListener('change', (e) => this.handleAudioDeviceChange(trackId, e));
        formatSelect.addEventListener('change', (e) => this.handleFormatChange(trackId, e));
        videoPreviewButton.addEventListener('click', () => this.togglePreview(trackId, 'video'));
        audioPreviewButton.addEventListener('click', () => this.togglePreview(trackId, 'audio'));
        removeButton.addEventListener('click', () => this.removeTrack(trackId));

        // 更新设备选项
        this.updateDeviceOptions(trackId);

        return trackElement;
    }

    async addTrack() {
        const trackId = this.nextTrackId++;
        this.tracks.set(trackId, {
            videoDevice: null,
            audioDevice: null,
            format: null
        });

        // 创建轨道元素
        const trackElement = this.createTrackElement(trackId);

        // 如果没有轨道了，禁用开始按钮
        this.elements.startButton.disabled = this.tracks.size === 0;

        return trackId;
    }

    removeTrack(trackId) {
        if (this.isRecording) {
            alert('录制过程中不能删除轨道');
            return;
        }

        // 如果正在预览，先停止预览
        if (this.previewSessions.has(trackId)) {
            this.togglePreview(trackId);
        }

        const trackElement = document.getElementById(`track-${trackId}`);
        if (trackElement) {
            trackElement.remove();
        }
        this.tracks.delete(trackId);

        // 如果没有轨道了，禁用开始按钮
        if (this.tracks.size === 0) {
            this.elements.startButton.disabled = true;
        }
    }

    updateDeviceOptions(trackId) {
        const trackElement = document.getElementById(`track-${trackId}`);
        if (!trackElement) return;

        const videoSelect = trackElement.querySelector('.video-device');
        const audioSelect = trackElement.querySelector('.audio-device');

        if (!videoSelect || !audioSelect) {
            console.error(`轨道 ${trackId} 的设备选择器未找到`);
            return;
        }

        try {
            // 更新视频设备选项
            videoSelect.innerHTML = '<option value="">请选择视频设备</option>' +
                this.devices.video
                    .map(device => `<option value="${device.id}">${device.name || device.id}</option>`)
                    .join('');

            // 更新音频设备选项
            audioSelect.innerHTML = '<option value="">请选择音频设备</option>' +
                this.devices.audio
                    .map(device => `<option value="${device.id}">${device.name || device.id}</option>`)
                    .join('');

            // 如果之前已经选择了设备，尝试保持选择
            const track = this.tracks.get(trackId);
            if (track) {
                if (track.videoDevice) {
                    videoSelect.value = track.videoDevice.id;
                }
                if (track.audioDevice) {
                    audioSelect.value = track.audioDevice.id;
                }
            }

            // 触发设备变更事件以更新格式选项
            if (videoSelect.value) {
                this.handleVideoDeviceChange(trackId, { target: videoSelect });
            }
        } catch (error) {
            console.error(`更新轨道 ${trackId} 的设备选项时出错:`, error);
        }
    }

    updateFormatOptions(trackId, videoDevice) {
        const trackElement = document.getElementById(`track-${trackId}`);
        if (!trackElement) return;

        const formatSelect = trackElement.querySelector('.video-format');
        formatSelect.innerHTML = '';

        if (videoDevice && videoDevice.videoFormats) {
            formatSelect.innerHTML = videoDevice.videoFormats
                .map((format, index) => `<option value="${index}">
                    ${format.resolution.width}x${format.resolution.height} @
                    ${format.frameRate.num}/${format.frameRate.den}fps
                </option>`)
                .join('');
        }
    }

    handleVideoDeviceChange(trackId, event) {
        const deviceId = event.target.value;
        const device = this.devices.video.find(d => d.id === deviceId);
        const track = this.tracks.get(trackId);
        const videoPreviewButton = document.querySelector(`#track-${trackId} .video-preview`);

        if (device) {
            track.videoDevice = device;
            this.updateFormatOptions(trackId, device);
            videoPreviewButton.disabled = false;
        } else {
            track.videoDevice = null;
            track.format = null;
            videoPreviewButton.disabled = true;
        }

        // 如果正在预览，停止预览
        if (this.previewSessions.has(trackId)) {
            this.togglePreview(trackId, 'video');
        }

        // 更新开始录制按钮状态
        this.updateStartButtonState();
    }

    handleAudioDeviceChange(trackId, event) {
        const deviceId = event.target.value;
        const device = this.devices.audio.find(d => d.id === deviceId);
        const track = this.tracks.get(trackId);
        const audioPreviewButton = document.querySelector(`#track-${trackId} .audio-preview`);

        if (device) {
            track.audioDevice = device;
            audioPreviewButton.disabled = false;
        } else {
            track.audioDevice = null;
            audioPreviewButton.disabled = true;
        }

        // 如果正在预览，停止预览
        if (this.previewSessions.has(`${trackId}-audio`)) {
            this.togglePreview(trackId, 'audio');
        }
    }

    handleFormatChange(trackId, event) {
        const formatIndex = parseInt(event.target.value);
        const track = this.tracks.get(trackId);
        if (track && track.videoDevice && track.videoDevice.videoFormats) {
            track.videoDevice.currentFormat = track.videoDevice.videoFormats[formatIndex];
        }
    }

    async startRecording() {
        if (this.tracks.size === 0) {
            this.updateStatus('请至少添加一个轨道', 'error');
            return;
        }

        try {
            // 构造录制配置
            const configs = [];
            for (const [trackId, track] of this.tracks) {
                if (!track.videoDevice || !track.audioDevice) {
                    throw new Error(`轨道 ${trackId} 未选择完整的设备`);
                }

                // 确保视频设备有currentFormat
                if (!track.videoDevice.currentFormat) {
                    if (track.videoDevice.videoFormats && track.videoDevice.videoFormats.length > 0) {
                        track.videoDevice.currentFormat = track.videoDevice.videoFormats[0];
                    } else {
                        throw new Error(`轨道 ${trackId} 的视频设备没有可用的格式`);
                    }
                }

                configs.push({
                    videoDevice: {
                        id: track.videoDevice.id,
                        name: track.videoDevice.name || '',
                        type: track.videoDevice.type,
                        isAvailable: true,
                        currentFormat: track.videoDevice.currentFormat,
                        path: track.videoDevice.path || ''
                    },
                    audioDevice: {
                        id: track.audioDevice.id,
                        name: track.audioDevice.name || '',
                        type: track.audioDevice.type,
                        isAvailable: true,
                        path: track.audioDevice.path || ''
                    }
                });
            }

            const recordingConfig = { configs };
            console.log('准备发送录制配置:', JSON.stringify(recordingConfig, null, 2));

            const message = {
                type: 'start_recording',
                payload: recordingConfig,
                messageId: Math.random().toString(36).substring(2)
            };
            console.log('构造的消息:', JSON.stringify(message, null, 2));

            await this.wsManager.send(message);

            this.elements.startButton.disabled = true;
            this.elements.stopButton.disabled = false;
            this.elements.addTrackButton.disabled = true;
            this.updateStatus('正在录制...', 'recording');
            this.isRecording = true;

        } catch (error) {
            console.error('启动录制失败:', error);
            this.updateStatus('启动录制失败: ' + error.message, 'error');
        }
    }

    async stopRecording() {
        if (!this.sessionId) {
            this.updateStatus('没有正在进行的录制会话', 'error');
            return;
        }

        try {
            const message = {
                type: 'stop_recording',
                payload: {
                    sessionId: this.sessionId
                },
                messageId: Math.random().toString(36).substring(2)
            };
            await this.wsManager.send(message);
        } catch (error) {
            this.updateStatus('停止录制失败: ' + error.message, 'error');
        }
    }

    handleRecordingStarted(data) {
        console.log('录制已开始:', data);
        this.sessionId = data.sessionId;
        
        // 为每个设备创建或更新视频轨道
        if (data.devices && data.devices.length > 0) {
            data.devices.forEach(async (config) => {
                try {
                    // 确保videoDevice是Device实例
                    if (config.videoDevice && !(config.videoDevice instanceof Device)) {
                        config.videoDevice = Device.fromJSON(config.videoDevice);
                    }

                    // 根据设备ID查找对应的轨道
                    let trackId = null;
                    for (const [id, track] of this.tracks.entries()) {
                        if (track.videoDevice && track.videoDevice.id === config.videoDevice.id) {
                            trackId = id;
                            break;
                        }
                    }

                    if (!trackId) {
                        console.error(`找不到设备 ${config.videoDevice.id} 对应的轨道`);
                        return;
                    }

                    console.log(`准备更新轨道:`, {
                        trackId,
                        deviceId: config.videoDevice.id,
                        deviceIndex: config.videoDevice.index,
                        config
                    });

                    await this.videoPlayer.updateTrack(trackId, config);
                    
                    console.log(`轨道已更新:`, {
                        trackId,
                        deviceId: config.videoDevice.id,
                        deviceIndex: config.videoDevice.index,
                        deviceIndexToTrackMap: Array.from(this.videoPlayer.deviceIndexToTrackMap.entries())
                    });
                } catch (error) {
                    console.error(`更新轨道失败:`, error);
                }
            });
        }

        this.updateStatus('录制已开始', 'recording');
    }

    handleRecordingStopped() {
        this.isRecording = false;
        this.sessionId = null;
        this.elements.startButton.disabled = false;
        this.elements.stopButton.disabled = true;
        this.elements.addTrackButton.disabled = false;
        this.updateStatus('录制已停止', 'ready');
    }

    handleError(error) {
        this.updateStatus('错误: ' + error.message, 'error');
    }

    updateStatus(message, className) {
        this.elements.status.textContent = message;
        this.elements.status.className = `status ${className}`;
    }

    // 切换预览状态
    async togglePreview(trackId, type = 'video') {
        const track = this.tracks.get(trackId);
        const previewButton = document.querySelector(`#track-${trackId} .${type}-preview`);
        const previewSessionId = type === 'audio' ? `${trackId}-audio` : trackId;
        const previewSession = this.previewSessions.get(previewSessionId);

        if (!previewSession) {
            // 开始预览
            const device = type === 'video' ? track.videoDevice : track.audioDevice;
            if (!device) {
                alert(`请先选择${type === 'video' ? '视频' : '音频'}设备`);
                return;
            }

            try {
                // 创建预览容器ID
                const containerId = `preview-${trackId}`;
                
                // 发送开始预览请求
                if (type === 'video') {
                    await this.wsManager.startPreview(device);
                } else {
                    await this.wsManager.startAudioPreview(device);
                }
                
                // 等待预览开始事件处理完成
                await new Promise(resolve => setTimeout(resolve, 100));
                
                // 创建预览播放器
                if (type === 'video') {
                    await this.videoPlayer.startPreview(device, containerId);
                } else {
                    await this.videoPlayer.startAudioPreview(device, containerId);
                }

                previewButton.textContent = `停止${type === 'video' ? '视频' : '音频'}预览`;
                previewButton.classList.add('active');
            } catch (error) {
                console.error('开始预览失败:', error);
                alert('开始预览失败: ' + error.message);
            }
        } else {
            // 停止预览
            try {
                await this.wsManager.stopPreview(previewSession.id);
                if (type === 'video') {
                    this.videoPlayer.stopPreview(track.videoDevice.id);
                } else {
                    this.videoPlayer.stopAudioPreview(track.audioDevice.id);
                }
                
                previewButton.textContent = `${type === 'video' ? '视频' : '音频'}预览`;
                previewButton.classList.remove('active');
                this.previewSessions.delete(previewSessionId);
            } catch (error) {
                console.error('停止预览失败:', error);
                alert('停止预览失败: ' + error.message);
            }
        }
    }

    // 处理预览开始事件
    handlePreviewStarted(data) {
        console.log('收到预览开始事件:', data);
        if (!data || !data.devices || !data.devices.length) {
            console.error('预览会话数据无效:', data);
            return;
        }

        let device = data.devices[0].videoDevice || data.devices[0].audioDevice;
        if (!device) {
            console.error('预览设备数据无效:', data);
            return;
        }

        const trackId = this.findTrackByDevice(device);
        if (trackId) {
            // 根据设备类型设置正确的trackId
            const isAudioDevice = device.type === 'microphone' || device.type === 'speaker';
            const previewSessionId = isAudioDevice ? `${trackId}-audio` : trackId;
            
            this.previewSessions.set(previewSessionId, {
                id: data.sessionId,
                device: device,
                startTime: data.startTime
            });
            
            // 根据设备类型调用不同的预览方法
            if (isAudioDevice) {
                // 音频预览不需要更新轨道
                console.log('音频预览会话已创建:', {
                    trackId: previewSessionId,
                    sessionId: data.sessionId,
                    device: device
                });
            } else {
                // 视频预览需要更新轨道
                this.videoPlayer.updateTrack(trackId, data.devices[0]);
            }
        } else {
            console.error('找不到匹配的轨道:', device);
        }
    }

    // 处理预览停止事件
    handlePreviewStopped(data) {
        console.log('收到预览停止事件:', data);
        
        // 遍历所有预览会话，找到匹配的sessionId
        for (const [trackId, session] of this.previewSessions.entries()) {
            if (session.id === data.sessionId) {
                // 停止预览播放器
                const track = this.tracks.get(trackId);
                if (track) {
                    const isAudioTrack = trackId.toString().endsWith('-audio');
                    if (isAudioTrack) {
                        this.videoPlayer.stopAudioPreview(track.audioDevice.id);
                    } else {
                        this.videoPlayer.stopPreview(track.videoDevice.id);
                    }
                }
                
                // 更新UI
                const type = trackId.toString().endsWith('-audio') ? 'audio' : 'video';
                const previewButton = document.querySelector(`#track-${trackId.toString().replace('-audio', '')} .${type}-preview`);
                if (previewButton) {
                    previewButton.textContent = `${type === 'video' ? '视频' : '音频'}预览`;
                    previewButton.classList.remove('active');
                }
                
                // 删除预览会话记录
                this.previewSessions.delete(trackId);
                break;
            }
        }
    }

    // 根据设备查找轨道ID
    findTrackByDevice(device) {
        for (const [trackId, track] of this.tracks.entries()) {
            if (track.videoDevice && track.videoDevice.id === device.id) {
                return trackId;
            }
            if (track.audioDevice && track.audioDevice.id === device.id) {
                return trackId;
            }
        }
        return null;
    }

    updateStartButtonState() {
        this.elements.startButton.disabled = this.tracks.size === 0;
    }
} 