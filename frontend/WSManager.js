import { WSMessageType, WSErrorType, WSReadyState, Device, DeviceType } from './WSTypes.js';

export class WSManager {
    constructor(config = {}) {
        this.config = {
            url: config.url || 'ws://' + window.location.host + '/ws',
            reconnectInterval: config.reconnectInterval || 3000,
            maxReconnectAttempts: config.maxReconnectAttempts || 5,
            messageTimeout: config.messageTimeout || 5000
        };

        this.ws = null;
        this.reconnectAttempts = 0;
        this.eventHandlers = new Map();
        this.pendingMessages = new Map();
        this.messageQueue = new Map();
        this.deviceCache = {
            video: [],
            audio: []
        };
        this.isConnecting = false;
        this.shouldReconnect = true;
        this.lastConnectTime = 0;
        this.minReconnectDelay = 1000; // 最小重连延迟1秒

        // 初始化事件处理器
        this.eventHandlers.set('devices', new Set());
        this.eventHandlers.set('binary', new Set());
        this.eventHandlers.set('error', new Set());
        this.eventHandlers.set('connected', new Set());
        this.eventHandlers.set('disconnected', new Set());
        this.eventHandlers.set('cleanup_completed', new Set());
        this.eventHandlers.set('recording_started', new Set());
        this.eventHandlers.set('recording_stopped', new Set());

        // 注册消息处理器
        this.handlers = {
            'preview_started': [],
            'preview_stopped': [],
            'stream_data': [],
            'error': []
        };

        // 绑定方法
        this.handleMessage = this.handleMessage.bind(this);
        this.reconnect = this.reconnect.bind(this);
        this.checkMessageAck = this.checkMessageAck.bind(this);
        this.cleanup = this.cleanup.bind(this);
        this.handleBinaryMessage = this.handleBinaryMessage.bind(this);
        this.send = this.send.bind(this);
        this.connect = this.connect.bind(this);

        // 初始化连接
        this.connect();

        // this.canvas = document.getElementById('canvas');
        // if (!this.canvas) {
        //     console.error('找不到canvas元素');
        //     return;
        // }
        // this.ctx = this.canvas.getContext('2d');
        // if (!this.ctx) {
        //     console.error('无法获取canvas上下文');
        //     return;
        // }
        // this.canvasContainer = document.getElementById('canvas-container');
        // console.log('test test');
        // this.videoPlayer = new VideoPlayer1();
        // console.log('videoPlayer', this.videoPlayer);
    }

    on(event, handler) {
        if (!this.eventHandlers.has(event)) {
            this.eventHandlers.set(event, new Set());
        }
        this.eventHandlers.get(event).add(handler);
        console.log(`已注册 ${event} 事件处理器`);
    }

    off(event, handler) {
        if (this.eventHandlers.has(event)) {
            this.eventHandlers.get(event).delete(handler);
        }
    }

    emit(event, data) {
        console.log(`正在触发 ${event} 事件:`, data);
        if (this.eventHandlers.has(event)) {
            const handlers = this.eventHandlers.get(event);
            handlers.forEach(handler => {
                try {
                    handler(data);
                } catch (error) {
                    console.error(`执行 ${event} 事件处理器时出错:`, error);
                }
            });
        }
    }

    connect() {
        // 如果已经连接，直接返回
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            return Promise.resolve();
        }

        // 如果正在连接中，返回现有的连接Promise
        if (this.connectPromise) {
            return this.connectPromise;
        }

        // 创建新的连接Promise
        this.connectPromise = new Promise((resolve, reject) => {
            try {
                this.ws = new WebSocket(this.config.url);

                // 设置二进制数据类型为ArrayBuffer
                this.ws.binaryType = 'arraybuffer';

                this.ws.onopen = () => {
                    console.log('WebSocket连接已建立');
                    this.reconnectAttempts = 0;
                    this.shouldReconnect = true;
                    this.emit('connected');
                    this.processPendingMessages();
                    this.connectPromise = null;
                    resolve();

                    this.ws.send(JSON.stringify({
                        type: 'get_session_id',
                        messageId: Math.random().toString(36).substring(2),
                    }))
                };

                this.ws.onclose = (event) => {
                    console.log('WebSocket连接已关闭:', event.code, event.reason);
                    this.emit('disconnected');
                    this.connectPromise = null;

                    // 只有在非正常关闭时才重连
                    if (event.code !== 1000 && this.shouldReconnect) {
                        this.reconnect();
                    }
                };

                this.ws.onerror = (error) => {
                    console.error('WebSocket错误:', error);
                    const wsError = new Error('WebSocket连接错误');
                    this.emit('error', wsError);
                    this.connectPromise = null;
                    reject(wsError);
                };

                this.ws.onmessage = this.handleMessage.bind(this);
            } catch (error) {
                console.error('创建WebSocket连接失败:', error);
                const connError = new Error('创建WebSocket连接失败');
                this.emit('error', connError);
                this.connectPromise = null;
                reject(connError);

                if (this.shouldReconnect) {
                    this.reconnect();
                }
            }
        });
    }

    reconnect() {
        if (!this.shouldReconnect || this.isConnecting || this.reconnectAttempts >= this.config.maxReconnectAttempts) {
            console.log('停止重连:', {
                shouldReconnect: this.shouldReconnect,
                isConnecting: this.isConnecting,
                attempts: this.reconnectAttempts,
                max: this.config.maxReconnectAttempts
            });
            return;
        }

        this.reconnectAttempts++;
        const delay = Math.min(
            this.config.reconnectInterval * Math.pow(1.5, this.reconnectAttempts - 1),
            30000 // 最大延迟30秒
        );

        console.log(`第 ${this.reconnectAttempts} 次重连，延迟 ${delay}ms`);

        setTimeout(() => {
            if (this.shouldReconnect && !this.isConnecting) {
                this.connect().catch(error => {
                    console.error('重连失败:', error);
                });
            }
        }, delay);
    }

    handleMessage(event) {
        try {
            // 检查是否是二进制数据
            if (event.data instanceof Blob || event.data instanceof ArrayBuffer) {
                // 确保数据是ArrayBuffer
                const processData = (data) => {
                    if (data instanceof Blob) {
                        return data.arrayBuffer();
                    }
                    return Promise.resolve(data);
                };

                processData(event.data)
                    .then(buffer => this.handleBinaryMessage(buffer))
                    .catch(error => {
                        console.error('处理二进制消息失败:', error);
                        this.emit('error', new Error(`处理二进制消息失败: ${error.message}`));
                    });
                return;
            }

            console.log('收到原始消息:', event.data);

            // 解析 JSON 消息
            const message = JSON.parse(event.data);
            console.log('解析后的消息:', message);

            // 根据消息类型处理
            if (message.type === 'devices' && message.payload) {
                this.handleDevicesMessage(message);
            } else if (message.type === 'ack') {
                this.handleAckMessage(message);
            } else if (message.type === 'error') {
                this.handleErrorMessage(message);
            } else if (message.type === 'recording_started') {
                this.handleRecordingStartedMessage(message);
            } else if (message.type === 'recording_stopped') {
                this.handleRecordingStoppedMessage(message);
            } else if (message.type === 'cleanup_completed') {
                this.handleCleanupCompletedMessage(message);
            } else if (message.type === 'preview_started') {
                this.handlePreviewStartedMessage(message);
            } else if (message.type === 'preview_stopped') {
                this.handlePreviewStoppedMessage(message);
            } else {
                console.warn('未知的消息类型:', message.type);
            }
        } catch (error) {
            console.error('处理WebSocket消息时出错:', error);
            this.emit('error', new Error(`处理WebSocket消息失败: ${error.message}`));
        }
    }

    handleBinaryMessage(buffer) {
        return new Promise((resolve, reject) => {
            try {
                if (!(buffer instanceof ArrayBuffer)) {
                    throw new Error('输入数据必须是ArrayBuffer');
                }

                const view = new DataView(buffer);
                let offset = 0;

                // 读取第一个字节，包含帧类型（高4位）和设备索引（低4位）
                const firstByte = view.getUint8(offset);
                const frameType = (firstByte & 0xF0) >> 4;  // 获取高4位作为帧类型
                const deviceIndex = firstByte & 0x0F;       // 获取低4位作为设备索引
                offset += 1;

                // 读取时间戳（3字节，大端序）
                const timestamp = (view.getUint8(offset + 2) << 12) |
                    (view.getUint8(offset + 1) << 4) |
                    (view.getUint8(offset) & 0x0F);
                offset += 3;

                // 剩余的数据是帧数据
                const frameData = buffer.slice(offset);

                // 添加详细日志
                console.log('解析二进制消息:', {
                    totalLength: buffer.byteLength,
                    firstByte: firstByte.toString(16).padStart(2, '0'),
                    frameType,
                    deviceIndex,
                    timestamp,
                    frameDataLength: frameData.byteLength,
                    firstBytes: Array.from(new Uint8Array(buffer).slice(0, 8))
                        .map(b => b.toString(16).padStart(2, '0'))
                        .join(' '),
                });

                // 发送解析后的数据给stream_data事件处理器
                this.emit('stream_data', {
                    frameType,
                    deviceIndex,
                    timestamp,
                    data: frameData
                });

                // 如果是视频帧，尝试渲染
                // if (frameType === 2 || frameType === 3) { //2和3表示实际视频帧
                //     console.log('渲染视频帧', this.videoPlayer, this);
                //     this.videoPlayer.handleFrame(frameData);
                // }

                resolve();
            } catch (error) {
                console.error('处理二进制消息时出错:', error);
                reject(error);
            }
        });
    }

    handleDevicesMessage(message) {
        console.log('开始处理设备列表消息');
        const payload = message.payload;
        if (!Array.isArray(payload.devices)) {
            console.error('设备列表格式错误:', payload);
            return;
        }

        const rawDevices = payload.devices;
        console.log('原始设备列表:', rawDevices);

        // 处理设备列表，保持原始格式
        const devices = rawDevices.map(device => ({
            id: device.id,
            name: device.name,
            type: device.type,
            isAvailable: device.isAvailable !== false,
            videoFormats: device.videoFormats || [],
            currentFormat: device.currentFormat || null,
            path: device.path || ''
        }));

        console.log('处理后的设备列表:', devices);

        // 更新设备缓存
        this.deviceCache = {
            video: devices.filter(d => d.type === 'camera' || d.type === 'screen'),
            audio: devices.filter(d => d.type === 'microphone' || d.type === 'system_audio')
        };

        console.log('设备缓存已更新:', this.deviceCache);

        // 发送设备列表事件
        this.emit('devices', devices);
        console.log('已触发设备列表事件');
    }

    handleRecordingStartedMessage(message) {
        console.log('收到录制开始消息:', message);
        
        let sessionData = null;
        
        if (message.data && message.data.sessionId) {
            sessionData = message.data;
        } else if (message.session && message.session.ID) {
            // 处理设备配置
            const processedConfigs = message.session.Configs.map(config => {
                // 处理视频设备
                const videoDevice = config.videoDevice ? {
                    id: config.videoDevice.id,
                    name: config.videoDevice.name,
                    type: config.videoDevice.type,
                    isAvailable: config.videoDevice.isAvailable !== false,
                    videoFormats: config.videoDevice.videoFormats || [],
                    currentFormat: config.videoDevice.currentFormat || null,
                    path: config.videoDevice.path || ''
                } : null;

                // 处理音频设备
                const audioDevice = config.audioDevice ? {
                    id: config.audioDevice.id,
                    name: config.audioDevice.name,
                    type: config.audioDevice.type,
                    isAvailable: config.audioDevice.isAvailable !== false,
                    path: config.audioDevice.path || ''
                } : null;

                return {
                    videoDevice,
                    audioDevice
                };
            });

            sessionData = {
                sessionId: message.session.ID,
                startTime: message.session.StartTime,
                devices: processedConfigs
            };
        }

        if (sessionData) {
            console.log('处理后的录制会话数据:', sessionData);
            this.emit('recording_started', sessionData);
        } else {
            console.warn('无效的录制开始消息格式:', message);
        }
    }

    handleRecordingStoppedMessage(message) {
        console.log('收到录制停止消息:', message);
        this.emit('recording_stopped', {
            sessionId: message.session ? message.session.ID : null,
            status: 'stopped'
        });
    }

    handleCleanupCompletedMessage(message) {
        console.log('收到清理完成消息:', message);
        this.emit('cleanup_completed', message.payload || {});
    }

    handlePreviewStartedMessage(message) {
        console.log('收到预览开始消息:', message);
        if (message.data && message.data.sessionId) {
            this.emit('preview_started', message.data);
        } else if (message.session && message.session.ID) {
            this.emit('preview_started', {
                sessionId: message.session.ID,
                startTime: message.session.StartTime,
                devices: message.session.Configs
            });
        }
    }

    handlePreviewStoppedMessage(message) {
        console.log('收到预览停止消息:', message);
        this.emit('preview_stopped', {
            sessionId: message.session ? message.session.ID : null,
            status: 'stopped'
        });
    }

    handleAckMessage(message) {
        if (!message.messageId) {
            console.warn('收到无效的确认消息');
            return;
        }

        this.handleMessageAck(message.messageId, message.status);
    }

    handleErrorMessage(message) {
        if (!message.payload) {
            console.warn('错误消息没有payload');
            return;
        }

        const error = new Error(`处理WebSocket消息失败: ${message.payload.message}`);
        console.error('收到错误消息:', message.payload);

        // 如果是"未找到录制会话"错误，不触发error事件
        if (message.payload.message === "未找到录制会话") {
            console.log('录制会话已结束');
            this.emit('recording_stopped', {});
            return;
        }

        this.emit('error', error);
    }

    handleMessageAck(messageId, status) {
        const pendingMessage = this.pendingMessages.get(messageId);
        if (pendingMessage) {
            clearTimeout(pendingMessage.timeout);
            this.pendingMessages.delete(messageId);

            if (status === 'success') {
                pendingMessage.resolve();
            } else {
                pendingMessage.reject(new Error(`消息确认失败: ${status}`));
            }
        }
    }

    async send(message) {
        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
            return this.queueMessage(message);
        }

        // 确保消息有类型
        if (!message.type) {
            throw new Error('消息必须包含类型');
        }

        // 生成消息ID
        const messageId = message.messageId || this.generateMessageId();

        // 构造完整的消息
        const wrappedMessage = {
            type: message.type,
            payload: message.payload || message.data || {},  // 兼容旧的data字段
            messageId: messageId
        };

        console.log('准备发送消息:', wrappedMessage);

        return new Promise((resolve, reject) => {
            // 如果是停止录制消息，延长超时时间
            const timeout = setTimeout(() => {
                this.pendingMessages.delete(messageId);
                if (message.type === 'stop_recording') {
                    // 如果是停止录制消息超时，仍然认为是成功的
                    console.log('停止录制消息超时，但继续处理');
                    resolve();
                } else {
                    reject(new Error('消息确认超时'));
                }
            }, message.type === 'stop_recording' ? this.config.messageTimeout * 2 : this.config.messageTimeout);

            this.pendingMessages.set(messageId, {
                message: wrappedMessage,
                timeout,
                resolve,
                reject,
                retries: 0
            });

            try {
                this.ws.send(JSON.stringify(wrappedMessage));
                console.log('消息已发送:', wrappedMessage);
            } catch (error) {
                clearTimeout(timeout);
                this.pendingMessages.delete(messageId);
                reject(new Error(`发送消息失败: ${error.message}`));
            }
        });
    }

    queueMessage(message) {
        return new Promise((resolve, reject) => {
            const messageId = this.generateMessageId();
            this.messageQueue.set(messageId, {
                message,
                resolve,
                reject,
                timestamp: Date.now()
            });
        });
    }

    processPendingMessages() {
        for (const [messageId, { message, resolve, reject }] of this.messageQueue) {
            this.send(message)
                .then(resolve)
                .catch(reject)
                .finally(() => this.messageQueue.delete(messageId));
        }
    }

    generateMessageId() {
        return Math.random().toString(36).substr(2, 9);
    }

    checkMessageAck() {
        const now = Date.now();
        for (const [messageId, { timestamp, reject }] of this.pendingMessages) {
            if (now - timestamp > this.config.messageTimeout) {
                this.pendingMessages.delete(messageId);
                reject(new Error('消息确认超时'));
            }
        }
    }

    close() {
        this.shouldReconnect = false;
        if (this.ws) {
            this.ws.close(1000, 'Normal closure');
            this.ws = null;
        }
    }

    cleanup() {
        // 清理WebSocket连接
        if (this.ws) {
            this.ws.onopen = null;
            this.ws.onclose = null;
            this.ws.onerror = null;
            this.ws.onmessage = null;
        }

        // 清理消息队列
        this.pendingMessages.forEach((pending) => {
            if (pending.timeout) {
                clearTimeout(pending.timeout);
            }
            if (pending.reject) {
                pending.reject(new Error('连接已关闭'));
            }
        });
        this.pendingMessages.clear();
        this.messageQueue.clear();

        // 重置状态
        this.isConnecting = false;
        this.ws = null;
    }

    // 发送预览请求
    async startPreview(device) {
        if (!device) {
            throw new Error('设备参数不能为空');
        }

        const message = {
            type: 'start_preview',
            payload: {
                videoDevice: {
                    id: device.id,
                    name: device.name,
                    type: device.type,
                    currentFormat: device.currentFormat,
                    path: device.path
                },
            }
        };
        return this.send(message);
    }

    async startAudioPreview(device) {
        if (!device) {
            throw new Error('设备参数不能为空');
        }

        const message = {
            type: 'start_preview',
            payload: {
                audioDevice: {
                    id: device.id,
                    name: device.name,
                    type: device.type,
                    currentFormat: device.currentFormat,
                    path: device.path
                },
            }
        };
        return this.send(message);
    }

    // 停止预览
    async stopPreview(sessionId) {
        if (!sessionId) {
            throw new Error('会话ID不能为空');
        }

        const message = {
            type: 'stop_preview',
            payload: {
                sessionId: sessionId
            }
        };

        return this.send(message);
    }
}