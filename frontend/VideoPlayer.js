// Device类定义
class Device {
    constructor(id, name, type, isAvailable = true, videoFormats = [], currentFormat = null, path = '') {
        this.id = id;
        this.name = name;
        this.type = type;
        this.isAvailable = isAvailable;
        this.videoFormats = videoFormats;
        this.currentFormat = currentFormat;
        this.path = path;
        
        // 根据deviceID动态计算设备索引
        this.index = this.calculateDeviceIndex(id);
    }

    // 计算设备索引，与后端保持一致
    calculateDeviceIndex(deviceId) {
        if (!deviceId) return 1; // 默认索引
        
        // 使用与后端相同的字符串求和哈希算法
        let sum = 0;
        for (let i = 0; i < deviceId.length; i++) {
            sum += deviceId.charCodeAt(i);
        }
        return (sum % 15) + 1; // 1-15，保留0用于特殊用途
    }

    toJSON() {
        return {
            id: this.id,
            name: this.name,
            type: this.type,
            isAvailable: this.isAvailable,
            videoFormats: this.videoFormats,
            currentFormat: this.currentFormat,
            path: this.path,
            index: this.index
        };
    }

    static fromJSON(json) {
        const device = new Device(
            json.id,
            json.name,
            json.type,
            json.isAvailable,
            json.videoFormats,
            json.currentFormat,
            json.path
        );
        return device;
    }
}

class MSEPlayer {
    constructor(videoElement) {
        this.videoElement = videoElement;
        this.mediaSource = null;
        this.sourceBuffer = null;
        this.queue = [];
        this.updating = false;
        this.isInitializing = false;
        this.pendingInitialization = false;
        this.currentFrameChunks = new Map();
        this.lastTimestamp = 0;
        this.frameBuffer = [];
        this.maxBufferSize = 30;
        this.initSegmentAppended = false;
        this.initSegment = null;
        this.timestampOffset = 0;
        this.errorCount = 0;
        this.maxErrorCount = 5;
        this.lastErrorTime = 0;
        this.errorResetInterval = 10000;
        this.pendingKeyframe = null;
        this.bufferUpdateInProgress = false;
        this.configurationReceived = false;
        this.firstKeyframeReceived = false;
        this.lastFrameTimestamp = 0;
        this.webmHeader = null;
        this.pendingFrames = [];
        this.frameQueue = [];
        this.waitingForInit = true;
        this.initializationPromise = null;
        this.initializationResolve = null;
        this.baseTimestamp = null;
        this.lastProcessedTimestamp = 0;
        this.destroyed = false;
        this.maxBufferLength = 30;
        this.recoveryAttempts = 0;
        this.maxRecoveryAttempts = 3;
        this.lastRecoveryTime = 0;
        this.recoveryInterval = 2000;
        this.isRecovering = false;
        this.operationQueue = [];
        this.isProcessingQueue = false;
        this.maxQueueSize = 100;
        this.lastQueueProcessTime = 0;
        this.queueProcessInterval = 10;
        this.lastSuccessfulAppend = 0;
        this.appendRetryCount = 0;
        this.maxAppendRetries = 3;
        this.appendRetryInterval = 100;
        this.errorCheckInterval = null;
        this.lastBufferUpdate = Date.now();
        this.bufferUpdateTimeout = 5000;
        this.isActive = true;
        this.maxBufferDuration = 60;
        this.minBufferDuration = 2;
        this.bufferAheadDuration = 30;
        this.lastBufferCheck = 0;
        this.bufferCheckInterval = 1000;
        this.mediaSourceStates = new Set(['open', 'ended', 'closed']);
        this.isEndOfStream = false;
        this.minFrameSize = 1;
        this.maxFrameSize = 10 * 1024 * 1024;
        this.timestampGapThreshold = 5000;
        this.errorDetails = new Map();
        this.consecutiveErrorCount = 0;
        this.maxConsecutiveErrors = 5;
        this.lastFrameType = null;
        this.initSegmentReceived = false;
        this.currentConfig = null;
        this.waitingForKeyframe = true;
        this.hasReceivedKeyframe = false;
        this.keyframeTimeout = 5000;
        this.keyframeTimer = null;
        this.isInitialized = false;
        this.hasInitSegment = false;
        this.hasKeyFrame = false;
        this.lastKeyFrameTimestamp = 0;
        this.minBufferTime = 0.5; // 最小缓冲时间（秒）
        
        console.log('Creating new MSEPlayer instance');

        // 设置视频元素属性
        this.videoElement.autoplay = true;
        this.videoElement.playsInline = true;
        this.videoElement.muted = true;
        this.videoElement.controls = true;

        // 添加错误事件监听
        this.videoElement.addEventListener('error', (e) => {
            console.error('视频元素错误:', {
                error: this.videoElement.error,
                errorCode: this.videoElement.error ? this.videoElement.error.code : null,
                errorMessage: this.videoElement.error ? this.videoElement.error.message : null
            });
        });

        // 添加状态变化监听
        this.videoElement.addEventListener('loadedmetadata', () => {
            console.log('视频元数据已加载:', {
                videoWidth: this.videoElement.videoWidth,
                videoHeight: this.videoElement.videoHeight,
                duration: this.videoElement.duration,
                readyState: this.videoElement.readyState
            });
        });

        this.videoElement.addEventListener('canplay', () => {
            console.log('视频可以播放:', {
                readyState: this.videoElement.readyState,
                paused: this.videoElement.paused,
                currentTime: this.videoElement.currentTime,
                buffered: Array.from(this.videoElement.buffered).map(i => ({
                    start: this.videoElement.buffered.start(i),
                    end: this.videoElement.buffered.end(i)
                }))
            });
            if (this.videoElement.paused) {
                this.videoElement.play().catch(e => {
                    console.warn('自动播放失败:', e);
                });
            }
        });

        this.videoElement.addEventListener('playing', () => {
            console.log('视频开始播放:', {
                readyState: this.videoElement.readyState,
                currentTime: this.videoElement.currentTime,
                videoWidth: this.videoElement.videoWidth,
                videoHeight: this.videoElement.videoHeight
            });
        });

        this.videoElement.addEventListener('waiting', () => {
            console.log('视频等待数据:', {
                readyState: this.videoElement.readyState,
                currentTime: this.videoElement.currentTime,
                buffered: Array.from(this.videoElement.buffered).map(i => ({
                    start: this.videoElement.buffered.start(i),
                    end: this.videoElement.buffered.end(i)
                }))
            });
        });

        // 定期检查视频状态
        setInterval(() => {
            if (!this.destroyed && this.isActive) {
                this.checkPlaybackStatus();
            }
        }, 1000);

        // 初始化 sourceOpenPromise
        this.sourceOpenPromise = new Promise((resolve, reject) => {
            this.sourceOpenResolve = resolve;
            this.sourceOpenReject = reject;
        });
    }

    async initialize() {
        try {
            console.log('开始初始化MSE播放器');
            
            // 创建MediaSource
            this.mediaSource = new MediaSource();
            
            // 创建 sourceOpenPromise
            this.sourceOpenPromise = new Promise((resolve, reject) => {
                const handleSourceOpen = () => {
                    console.log('MediaSource sourceopen 事件触发:', {
                        readyState: this.mediaSource.readyState,
                        duration: this.mediaSource.duration,
                        videoElement: {
                            readyState: this.videoElement.readyState,
                            networkState: this.videoElement.networkState,
                            error: this.videoElement.error,
                            src: this.videoElement.src
                        }
                    });

                    // 设置初始duration
                    try {
                        this.mediaSource.duration = 100;
                        console.log('设置初始duration:', this.mediaSource.duration);
                    } catch (e) {
                        console.warn('设置初始duration失败:', e);
                    }

                    // 立即创建SourceBuffer
                    try {
                        // 根据元素类型选择合适的MIME类型
                        const isAudioElement = this.videoElement.tagName.toLowerCase() === 'audio';
                        const isWindows = navigator.userAgent.includes('Windows');
                        
                        // Windows环境下优先使用VP8编码器
                        const defaultVideoCodec = isWindows ? 'vp8' : 'vp9';
                        const mimeType = isAudioElement ? 
                            'audio/webm;codecs=opus' : 
                            `video/webm;codecs=vp9`;

                        if (!MediaSource.isTypeSupported(mimeType)) {
                            console.warn('不支持的MIME类型:', mimeType);
                            // 尝试其他MIME类型
                            const alternativeMimeTypes = isAudioElement ? 
                                ['audio/webm;codecs=opus', 'audio/webm'] :
                                [`video/webm;codecs=${defaultVideoCodec}`,'video/webm'];

                            let supported = false;
                            for (const type of alternativeMimeTypes) {
                                if (MediaSource.isTypeSupported(type)) {
                                    console.log('使用替代MIME类型:', type);
                                    this.sourceBuffer = this.mediaSource.addSourceBuffer(type);
                                    supported = true;
                                    break;
                                }
                            }
                            if (!supported) {
                                reject(new Error('没有找到支持的MIME类型'));
                                return;
                            }
                        } else {
                            this.sourceBuffer = this.mediaSource.addSourceBuffer(mimeType);
                        }

                        // 设置sourceBuffer的模式和时间戳偏移
                        this.sourceBuffer.mode = 'segments';
                        this.sourceBuffer.timestampOffset = 0;

                        console.log('SourceBuffer创建成功:', {
                            mode: this.sourceBuffer.mode,
                            updating: this.sourceBuffer.updating,
                            timestampOffset: this.sourceBuffer.timestampOffset,
                            mimeType: mimeType,
                            platform: navigator.userAgent
                        });

                        // 移除事件监听器
                        this.mediaSource.removeEventListener('sourceopen', handleSourceOpen);
                        resolve();
                    } catch (error) {
                        console.error('创建SourceBuffer失败:', error);
                        reject(error);
                    }
                };

                this.mediaSource.addEventListener('sourceopen', handleSourceOpen);
            });
            
            // 设置视频源
            const mediaSourceUrl = URL.createObjectURL(this.mediaSource);
            console.log('创建MediaSource URL:', mediaSourceUrl);
            this.videoElement.src = mediaSourceUrl;
            
            // 等待MediaSource打开
            await this.sourceOpenPromise;

            // 尝试自动播放
            try {
                // await this.videoElement.play();
                console.log('自动播放成功');
            } catch (error) {
                console.warn('自动播放失败,需要用户交互:', error);
            }

            console.log('MSE播放器初始化完成');
            return true;
        } catch (error) {
            console.error('MSE初始化失败:', error);
            return false;
        }
    }

    async waitForSourceOpen() {
        try {
            await this.sourceOpenPromise;
            return true;
        } catch (error) {
            console.error('等待 MediaSource 打开失败:', error);
            return false;
        }
    }

    async initSourceBuffer() {
        if (this.sourceBuffer) {
            console.log('SourceBuffer已存在');
            return;
        }

        // 确保MediaSource已打开
        if (this.mediaSource.readyState !== 'open') {
            console.log('等待MediaSource打开...');
            await this.sourceOpenPromise;
        }

        const mimeType = 'video/webm; codecs="vp9"';
        console.log('创建SourceBuffer:', {
            mimeType,
            supported: MediaSource.isTypeSupported(mimeType),
            mediaSourceState: this.mediaSource.readyState,
            videoElement: {
                readyState: this.videoElement.readyState,
                networkState: this.videoElement.networkState,
                error: this.videoElement.error,
                src: this.videoElement.src
            }
        });

        if (!MediaSource.isTypeSupported(mimeType)) {
            throw new Error(`不支持的MIME类型: ${mimeType}`);
        }

        return new Promise((resolve, reject) => {
        try {
                // 创建SourceBuffer
            this.sourceBuffer = this.mediaSource.addSourceBuffer(mimeType);
            this.sourceBuffer.mode = 'segments';
            
                // 设置时间戳偏移为0
                this.sourceBuffer.timestampOffset = 0;
                
                // 监听updateend事件
                const handleUpdateEnd = () => {
                    console.log('SourceBuffer初始化完成:', {
                        updating: this.sourceBuffer.updating,
                        mode: this.sourceBuffer.mode,
                        timestampOffset: this.sourceBuffer.timestampOffset,
                        buffered: this.sourceBuffer.buffered.length > 0 ? {
                            start: this.sourceBuffer.buffered.start(0),
                            end: this.sourceBuffer.buffered.end(0)
                        } : 'empty',
                        videoElement: {
                            readyState: this.videoElement.readyState,
                            networkState: this.videoElement.networkState,
                            error: this.videoElement.error,
                            src: this.videoElement.src,
                            videoWidth: this.videoElement.videoWidth,
                            videoHeight: this.videoElement.videoHeight
                        }
                    });
                    
                    // 移除一次性事件监听器
                    this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                    this.sourceBuffer.removeEventListener('error', handleError);
                    
                    // 添加持续的updateend监听器
                    this.sourceBuffer.addEventListener('updateend', () => {
                        if (!this.sourceBuffer.updating) {
                            // 处理队列中的数据
                            while (this.queue.length > 0 && !this.sourceBuffer.updating) {
                                const frame = this.queue.shift();
                                this.appendBuffer(frame.data).catch(error => {
                                    console.error('处理队列数据失败:', error);
                                    if (error.name === 'QuotaExceededError') {
                                        this.handleQuotaExceeded().then(() => {
                                            // 将当前数据重新加入队列
                                            this.queue.unshift(frame);
                                        });
                                    }
                                });
                            }
                        }
                    });
                    
                    resolve();
                };

                // 监听error事件
                const handleError = (error) => {
                    console.error('SourceBuffer初始化错误:', error);
                    this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                    this.sourceBuffer.removeEventListener('error', handleError);
                    reject(error);
                };

                // 添加事件监听器
                this.sourceBuffer.addEventListener('updateend', handleUpdateEnd);
                this.sourceBuffer.addEventListener('error', handleError);

                console.log('SourceBuffer创建成功');
        } catch (error) {
                console.error('创建SourceBuffer失败:', error);
                reject(error);
        }
        });
    }

    startHealthCheck() {
        if (this.errorCheckInterval) {
            clearInterval(this.errorCheckInterval);
        }
        
        this.errorCheckInterval = setInterval(() => {
            if (this.isActive) {
                this.checkBufferHealth();
            }
        }, 1000);
    }

    stopHealthCheck() {
        if (this.errorCheckInterval) {
            clearInterval(this.errorCheckInterval);
            this.errorCheckInterval = null;
        }
    }

    async checkBufferHealth() {
        if (!this.sourceBuffer || this.destroyed || this.isRecovering || !this.isActive) {
            return;
        }

        const now = Date.now();
        if (this.initSegmentAppended && now - this.lastBufferUpdate > this.bufferUpdateTimeout) {
            console.warn('缓冲区更新超时，尝试恢复');
            await this.recover();
            return;
        }

        try {
            if (this.sourceBuffer.buffered.length > 0) {
                const bufferedEnd = this.sourceBuffer.buffered.end(this.sourceBuffer.buffered.length - 1);
                const currentTime = this.videoElement.currentTime;
                
                if (this.videoElement.played.length > 0 && bufferedEnd < currentTime - 5) {
                    console.warn('缓冲区落后于播放时间，尝试恢复');
                    await this.recover();
                }
            }
        } catch (error) {
            console.warn('检查缓冲区健康状态时出错:', error);
        }
    }

    async recover() {
        if (this.isRecovering || !this.isActive) {
            console.log('恢复操作已在进行中或播放器不活跃');
            return;
        }

        console.log('尝试恢复播放器状态...');
        this.isRecovering = true;

        try {
            const now = Date.now();
            if (now - this.lastRecoveryTime < this.recoveryInterval) {
                console.log('恢复尝试过于频繁，等待间隔');
                return;
            }

            this.lastRecoveryTime = now;
            this.recoveryAttempts++;

            if (this.recoveryAttempts > this.maxRecoveryAttempts) {
                console.warn('超过最大恢复尝试次数，重建 MediaSource');
                await this.rebuildMediaSource();
                this.recoveryAttempts = 0;
                return;
            }

            this.bufferUpdateInProgress = false;
            this.isProcessingQueue = false;
            this.operationQueue = [];
            
            if (!this.sourceBuffer || !this.mediaSource || this.mediaSource.readyState !== 'open') {
                console.log('MediaSource 或 SourceBuffer 无效，重建组件');
                await this.rebuildMediaSource();
                return;
            }

            await this.initSourceBuffer();
            
            if (this.initSegment) {
                await this.appendInitSegment(this.initSegment);
            }

            this.lastBufferUpdate = Date.now();
            console.log('恢复完成');
        } catch (e) {
            console.error('恢复过程中出错:', e);
            await this.rebuildMediaSource();
        } finally {
            this.isRecovering = false;
        }
    }

    reset() {
        console.log('重置播放器状态');
        this.isActive = false;
        this.stopHealthCheck();

        if (this.sourceBuffer && this.mediaSource && this.mediaSource.readyState === 'open') {
            try {
                if (this.sourceBuffer.updating) {
                    this.sourceBuffer.abort();
                }
                this.mediaSource.removeSourceBuffer(this.sourceBuffer);
            } catch (e) {
                console.warn('清理 SourceBuffer 时出错:', e);
            }
        }

        this.sourceBuffer = null;
        this.queue = [];
        this.updating = false;
        this.isInitializing = false;
        this.pendingInitialization = false;
        this.currentFrameChunks = new Map();
        this.lastTimestamp = 0;
        this.frameBuffer = [];
        this.initSegmentAppended = false;
        this.initSegment = null;
        this.timestampOffset = 0;
        this.errorCount = 0;
        this.lastErrorTime = 0;
        this.pendingKeyframe = null;
        this.bufferUpdateInProgress = false;
        this.configurationReceived = false;
        this.firstKeyframeReceived = false;
        this.lastFrameTimestamp = 0;
        this.webmHeader = null;
        this.pendingFrames = [];
        this.frameQueue = [];
        this.waitingForInit = true;
        this.baseTimestamp = null;
        this.lastProcessedTimestamp = 0;
        this.destroyed = false;
        this.recoveryAttempts = 0;
        this.lastRecoveryTime = 0;
        this.isRecovering = false;
        this.operationQueue = [];
        this.isProcessingQueue = false;
        this.lastQueueProcessTime = 0;
        this.lastSuccessfulAppend = 0;
        this.appendRetryCount = 0;

        this.isActive = true;
        this.startHealthCheck();

        // 重新初始化 sourceOpenPromise
        this.sourceOpenPromise = new Promise((resolve, reject) => {
            this.sourceOpenResolve = resolve;
            this.sourceOpenReject = reject;
        });
    }

    normalizeTimestamp(timestamp) {
        if (this.baseTimestamp === null) {
            this.baseTimestamp = timestamp;
            return 0;
        }
        // 将时间戳转换为毫秒
        return Math.max(0, (timestamp - this.baseTimestamp));
    }

    async waitForSourceBuffer() {
        if (!this.sourceBuffer || this.destroyed || this.isRecovering) {
            return false;
        }

        const maxWaitTime = 1000; // 最大等待时间1秒
        const startTime = Date.now();

        while (this.sourceBuffer.updating) {
            if (Date.now() - startTime > maxWaitTime) {
                console.warn('等待 SourceBuffer 就绪超时');
                return false;
            }
            await new Promise(resolve => setTimeout(resolve, 10));
        }

        return true;
    }

    async processOperationQueue() {
        if (this.isProcessingQueue || !this.sourceBuffer || this.destroyed || this.isRecovering) {
            return;
        }

        this.isProcessingQueue = true;

        try {
            while (this.operationQueue.length > 0) {
                // 检查 MediaSource 状态
                if (this.mediaSource.readyState !== 'open') {
                    console.warn('MediaSource 未处于 open 状态，暂停处理队列');
                    break;
                }

                if (!await this.waitForSourceBuffer()) {
                    break;
                }

                const operation = this.operationQueue[0];
                
                try {
                    if (operation.type === 'append') {
                        await this.appendBufferWithRetry(operation.data);
                    } else if (operation.type === 'remove') {
                        await this.removeBufferWithRetry(operation.start, operation.end);
                    }

                    this.lastBufferUpdate = Date.now();
                    this.appendRetryCount = 0;
                    this.operationQueue.shift();
                } catch (error) {
                    console.error('队列操作失败:', error);
                    
                    if (error.name === 'QuotaExceededError') {
                        await this.handleQuotaExceeded();
                        continue;
                    }

                    this.appendRetryCount++;
                    if (this.appendRetryCount >= this.maxAppendRetries) {
                        console.error('超过最大重试次数，放弃当前操作');
                        this.operationQueue.shift();
                        this.appendRetryCount = 0;
                        await this.recover();
                        break;
                    } else {
                        await new Promise(resolve => setTimeout(resolve, this.appendRetryInterval));
                    }
                }
            }
        } catch (error) {
            console.error('队列处理出错:', error);
            await this.handleError(error);
        } finally {
            this.isProcessingQueue = false;
        }
    }

    async appendBufferWithRetry(data) {
        return new Promise((resolve, reject) => {
            const handleUpdateEnd = () => {
                this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                this.sourceBuffer.removeEventListener('error', handleError);
                resolve();
            };

            const handleError = (error) => {
                this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                this.sourceBuffer.removeEventListener('error', handleError);
                reject(error);
            };

            try {
                this.sourceBuffer.addEventListener('updateend', handleUpdateEnd);
                this.sourceBuffer.addEventListener('error', handleError);
                this.sourceBuffer.appendBuffer(data);
            } catch (error) {
                this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                this.sourceBuffer.removeEventListener('error', handleError);
                reject(error);
            }
        });
    }

    async removeBufferWithRetry(start, end) {
        return new Promise((resolve, reject) => {
            const handleUpdateEnd = () => {
                this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                this.sourceBuffer.removeEventListener('error', handleError);
                resolve();
            };

            const handleError = (error) => {
                this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                this.sourceBuffer.removeEventListener('error', handleError);
                reject(error);
            };

            try {
                this.sourceBuffer.addEventListener('updateend', handleUpdateEnd);
                this.sourceBuffer.addEventListener('error', handleError);
                this.sourceBuffer.remove(start, end);
            } catch (error) {
                this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                this.sourceBuffer.removeEventListener('error', handleError);
                reject(error);
            }
        });
    }

    async appendMediaChunk(chunk, timestamp) {
        if (!chunk || !(chunk instanceof Uint8Array) || chunk.length === 0) {
            console.warn('无效的数据块');
            return;
        }

        if (this.destroyed || this.isRecovering) {
            console.warn('MediaSource 已销毁或正在恢复，忽略数据块');
            return;
        }

        if (!this.sourceBuffer || this.mediaSource.readyState !== 'open') {
            throw new Error('SourceBuffer或MediaSource未就绪');
        }

        try {
            // 等待 sourceBuffer 就绪
            if (this.sourceBuffer.updating) {
                await new Promise((resolve, reject) => {
                    const handleUpdateEnd = () => {
                        this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                        this.sourceBuffer.removeEventListener('error', handleError);
                        resolve();
                    };
                    const handleError = (error) => {
                        this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                        this.sourceBuffer.removeEventListener('error', handleError);
                        reject(error);
                    };
                    this.sourceBuffer.addEventListener('updateend', handleUpdateEnd);
                    this.sourceBuffer.addEventListener('error', handleError);
                });
            }

            // 处理时间戳
            const normalizedTimestamp = this.normalizeTimestamp(timestamp);
            
            // 如果是第一个数据块,设置时间戳偏移
            if (!this.hasFirstChunk) {
                this.sourceBuffer.timestampOffset = 0;
                this.firstTimestamp = normalizedTimestamp;
                this.hasFirstChunk = true;
            }

            // 添加数据块
            await this.appendBuffer(chunk);
            
            // 检查缓冲区状态
            if (this.sourceBuffer.buffered.length > 0) {
                const bufferedEnd = this.sourceBuffer.buffered.end(0);
                const currentTime = this.videoElement.currentTime;
                
                // 如果视频未开始播放且有足够的数据，尝试播放
                if (this.videoElement.paused && bufferedEnd - currentTime >= 0.5) {
                    try {
                        await this.tryPlay();
                    } catch (e) {
                        console.warn('自动播放失败:', e);
                    }
                }
                
                // 如果当前播放位置落后太多，尝试跳转
                const minPlaybackOffset = 0.3; // 最小播放延迟
                const maxPlaybackOffset = 1.0; // 最大播放延迟
                const currentOffset = bufferedEnd - currentTime;
                
                if (currentOffset > maxPlaybackOffset && !this.videoElement.paused) {
                    const targetTime = bufferedEnd - minPlaybackOffset;
                    console.log('播放延迟过大,跳转到:', targetTime);
                    this.videoElement.currentTime = targetTime;
                }
            }

            // 维护缓冲区
            await this.maintainBuffer();

        } catch (e) {
            console.error('添加数据块失败:', e);
            if (e.name === 'QuotaExceededError') {
                await this.handleQuotaExceeded();
            } else {
                throw e;
            }
        }
    }

    async maintainBuffer() {
        if (!this.sourceBuffer || this.sourceBuffer.updating) {
            return;
        }

        try {
            const buffered = this.sourceBuffer.buffered;
            if (buffered.length > 0) {
                const currentTime = this.videoElement.currentTime;
                const bufferStart = buffered.start(0);
                const bufferEnd = buffered.end(0);
                
                // 如果缓冲区过大或者起始点太旧，移除旧的数据
                const maxBufferSize = 10; // 最大缓冲区大小(秒)
                const minBufferSize = 3;  // 最小缓冲区大小(秒)
                const bufferSize = bufferEnd - bufferStart;
                
                if (bufferSize > maxBufferSize || (currentTime - bufferStart) > maxBufferSize) {
                    const removeEnd = Math.max(
                        currentTime - minBufferSize,  // 保留当前播放位置前的少量缓冲
                        bufferStart + (bufferSize - maxBufferSize) // 或者移除超出最大缓冲的部分
                    );
                    
                    if (removeEnd > bufferStart && removeEnd < bufferEnd) {
                        console.log('清理缓冲区:', {
                            bufferStart,
                            bufferEnd,
                            currentTime,
                            removeEnd,
                            bufferSize
                        });
                        await this.removeBufferWithRetry(bufferStart, removeEnd);
                    }
                }
            }
        } catch (e) {
            console.warn('维护缓冲区时出错:', e);
        }
    }

    async processQueue() {
        if (this.isProcessingQueue || !this.sourceBuffer || this.sourceBuffer.updating) {
            return;
        }

        this.isProcessingQueue = true;

        try {
            while (this.queue.length > 0 && !this.sourceBuffer.updating) {
                const frame = this.queue.shift();
                await this.appendBuffer(frame.data);
            }
        } catch (error) {
            console.error('处理队列错误:', error);
        } finally {
            this.isProcessingQueue = false;
        }
    }
    
    async handleStreamData({ frameType, timestamp, data }) {
        if (!data) {
            console.warn('无效的帧数据');
            return;
        }

        try {
            const rawData = data instanceof Uint8Array ? data : new Uint8Array(data);
            
            switch (frameType) {
                case 1: // 初始化段
                case 4:
                    console.log('MSEPlayer处理初始化段:', {
                        size: rawData.length,
                        firstBytes: Array.from(rawData.slice(0, 16))
                            .map(b => b.toString(16).padStart(2, '0'))
                            .join(' ')
                    });
                    await this.handleInitSegment(rawData);
                    break;

                case 2: // 关键帧
                    console.log('MSEPlayer处理关键帧:', {
                        size: rawData.length,
                        timestamp,
                        firstBytes: Array.from(rawData.slice(0, 16))
                            .map(b => b.toString(16).padStart(2, '0'))
                            .join(' ')
                    });
                    await this.appendMediaChunk(rawData, timestamp);
                    this.hasKeyFrame = true;
                    this.lastKeyFrameTimestamp = timestamp;
                    break;

                case 3: // 普通帧
                case 5:
                    await this.appendMediaChunk(rawData, timestamp);
                    break;

                default:
                    console.warn('未知的帧类型:', frameType);
            }
        } catch (error) {
            console.error('MSEPlayer处理帧数据失败:', error);
            throw error;
        }
    }

    async handleInitSegment(chunk) {
        if (!this.sourceBuffer || this.mediaSource.readyState !== 'open') {
            throw new Error('SourceBuffer或MediaSource未就绪');
        }

        try {
            // 验证WebM头部
            if (!this.validateWebMHeader(chunk)) {
                throw new Error('无效的WebM头部');
            }

            // 设置初始duration
            if (isNaN(this.mediaSource.duration)) {
                this.mediaSource.duration = 100;
            }

            await this.appendBuffer(chunk);
            this.hasInitSegment = true;
            console.log('初始化段添加成功');

            // 处理积累的帧
            await this.processPendingFrames();
        } catch (error) {
            console.error('添加初始化段失败:', error);
            throw error;
        }
    }

    async processPendingFrames() {
        if (!this.hasInitSegment) {
            return;
        }

        // 首先处理关键帧
        const keyFrames = this.pendingFrames.filter(frame => frame.frameType === 2);
        if (keyFrames.length > 0) {
            for (const frame of keyFrames) {
                await this.appendMediaChunk(frame.data, frame.timestamp);
                this.hasKeyFrame = true;
                this.lastKeyFrameTimestamp = frame.timestamp;
            }
        }

        // 如果有关键帧，处理普通帧
        if (this.hasKeyFrame) {
            const normalFrames = this.pendingFrames.filter(frame => frame.frameType === 3);
            for (const frame of normalFrames) {
                await this.appendMediaChunk(frame.data, frame.timestamp);
            }
        }

        // 清空处理过的帧
        this.pendingFrames = [];
    }

    async appendInitSegment() {
        if (!this.initSegment || this.initSegmentAppended) {
            return;
        }

        try {
            await this.appendBuffer(this.initSegment);
            this.initSegmentAppended = true;
            console.log('初始化段添加成功');
            
            // 处理队列中的数据
            await this.processQueue();
        } catch (error) {
            console.error('添加初始化段失败:', error);
            throw error;
        }
    }

    async appendBuffer(chunk) {
        if (!this.sourceBuffer) {
            throw new Error('SourceBuffer未初始化');
        }

        // if (this.sourceBuffer.updating) {
        //     throw new Error('SourceBuffer正在更新中');
        // }

        if (!chunk || chunk.length === 0) {
            throw new Error('无效的数据块');
        }

        if (this.mediaSource.readyState !== 'open') {
            throw new Error('MediaSource未处于open状态');
        }

        // 等待 SourceBuffer 完成当前更新
        if (this.sourceBuffer.updating) {
            await new Promise((resolve, reject) => {
                const handleUpdateEnd = () => {
                    this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                    this.sourceBuffer.removeEventListener('error', handleError);
                    resolve();
                };

                const handleError = (error) => {
                    this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                    this.sourceBuffer.removeEventListener('error', handleError);
                    reject(error);
                };

                this.sourceBuffer.addEventListener('updateend', handleUpdateEnd);
                this.sourceBuffer.addEventListener('error', handleError);
            });
        }

        console.log('添加数据块:', {
            size: chunk.length,
            firstBytes: Array.from(chunk.slice(0, 16))
                .map(b => b.toString(16).padStart(2, '0'))
                .join(' '),
            sourceBufferState: {
                updating: this.sourceBuffer.updating,
                mode: this.sourceBuffer.mode,
                timestampOffset: this.sourceBuffer.timestampOffset,
                buffered: this.sourceBuffer.buffered.length > 0 ? {
                    start: this.sourceBuffer.buffered.start(0),
                    end: this.sourceBuffer.buffered.end(0)
                } : 'empty'
            }
        });

        return new Promise((resolve, reject) => {
            const handleUpdateEnd = () => {
                this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                this.sourceBuffer.removeEventListener('error', handleError);

                // 检查缓冲区状态
                if (this.sourceBuffer.buffered.length > 0) {
                    const bufferedEnd = this.sourceBuffer.buffered.end(0);
                    const bufferedStart = this.sourceBuffer.buffered.start(0);
                    console.log('数据块添加完成:', {
                        buffered: {
                            start: bufferedStart,
                            end: bufferedEnd,
                            duration: bufferedEnd - bufferedStart
                        }
                    });

                    // 如果视频暂停且有足够的缓冲，尝试播放
                    if (this.videoElement.paused && bufferedEnd - bufferedStart >= this.minBufferTime) {
                        this.tryPlay();
                    }
                }
                
                resolve();
            };

            const handleError = (error) => {
                this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                this.sourceBuffer.removeEventListener('error', handleError);
                console.error('添加数据块失败:', error);
                reject(error);
            };

            try {
                // 添加事件监听器
                this.sourceBuffer.addEventListener('updateend', handleUpdateEnd);
                this.sourceBuffer.addEventListener('error', handleError);

                // 添加数据
                this.sourceBuffer.appendBuffer(chunk);
            } catch (error) {
                this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                this.sourceBuffer.removeEventListener('error', handleError);
                reject(error);
            }
        });
    }

    async processQueue() {
        if (this.isProcessingQueue || !this.sourceBuffer || this.sourceBuffer.updating) {
            return;
        }

        this.isProcessingQueue = true;

        try {
            while (this.queue.length > 0 && !this.sourceBuffer.updating) {
                const frame = this.queue.shift();
                await this.appendBuffer(frame.data);
            }
        } catch (error) {
            console.error('处理队列错误:', error);
        } finally {
            this.isProcessingQueue = false;
        }
    }
    
    async handleQuotaExceeded() {
        if (!this.sourceBuffer || !this.sourceBuffer.buffered.length) {
            return;
        }

        const currentTime = this.videoElement.currentTime;
        const bufferStart = this.sourceBuffer.buffered.start(0);
        const bufferEnd = this.sourceBuffer.buffered.end(0);

        // 如果缓冲区超过30秒，删除旧的数据
        if (bufferEnd - bufferStart > 30) {
            try {
                await new Promise((resolve, reject) => {
                    const handleUpdateEnd = () => {
                        this.sourceBuffer.removeEventListener('updateend', handleUpdateEnd);
                        resolve();
                    };
                    this.sourceBuffer.addEventListener('updateend', handleUpdateEnd);
                    this.sourceBuffer.remove(bufferStart, currentTime - 1);
                });
            } catch (error) {
                console.error('清理缓冲区失败:', error);
            }
        }
    }
    
    async handleError(error) {
        if (this.destroyed) {
            console.warn('播放器已销毁，忽略错误处理');
            return;
        }

        console.error('处理错误:', {
            error,
            details: this.errorDetails,
            sourceBufferState: this.sourceBuffer ? {
                updating: this.sourceBuffer.updating,
                mode: this.sourceBuffer.mode,
                buffered: this.sourceBuffer.buffered.length
            } : 'null',
            mediaSourceState: this.mediaSource ? this.mediaSource.readyState : 'null',
            lastFrameType: this.lastFrameType,
            consecutiveErrors: this.consecutiveErrorCount
        });

        try {
            const now = Date.now();
            if (now - this.lastErrorTime > this.errorResetInterval) {
                this.errorCount = 0;
            }
            this.lastErrorTime = now;
            
            this.errorCount++;
            if (this.errorCount >= this.maxErrorCount) {
                console.warn('错误次数过多，重建 MediaSource');
                await this.rebuildMediaSource();
                this.errorCount = 0;
                this.errorDetails.clear();
            } else {
                await this.recover();
            }
        } catch (e) {
            console.error('错误处理失败:', e);
            this.reset();
        }
    }
    
    async rebuildMediaSource() {
        return new Promise((resolve) => {
            const oldMediaSource = this.mediaSource;
            this.mediaSource = new MediaSource();
            this.reset();
            
            // 重新初始化 sourceOpenPromise
            this.sourceOpenPromise = new Promise((resolve, reject) => {
                this.sourceOpenResolve = resolve;
                this.sourceOpenReject = reject;
            });

            this.mediaSource.addEventListener('sourceopen', () => {
                this.initSourceBuffer();
                if (this.initSegment) {
                    this.appendInitSegment(this.initSegment);
                }
                resolve();
            });
            
            this.videoElement.src = URL.createObjectURL(this.mediaSource);
            
            if (oldMediaSource && oldMediaSource.readyState === 'open') {
                try {
                    oldMediaSource.endOfStream();
                } catch (e) {
                    console.warn('结束旧 MediaSource 失败:', e);
                }
            }
        });
    }

    validateFrame(frameData) {
        const { frameType, timestamp, data } = frameData;
        const size = data.byteLength;

        try {
            // 检查帧大小
            if (size < this.minFrameSize || size > this.maxFrameSize) {
                throw new Error(`无效的帧大小: ${size} bytes`);
            }

            // 检查时间戳连续性
            if (this.lastValidTimestamp > 0) {
                const timestampDiff = Math.abs(timestamp - this.lastValidTimestamp);
                if (timestampDiff > this.timestampGapThreshold) {
                    console.warn(`时间戳间隔较大: ${timestampDiff}ms，但继续处理`);
                }
            }

            // 检查帧类型
            const actualFrameType = frameType * 0x10;
            if (actualFrameType !== 0x10 && actualFrameType !== 0x20 && actualFrameType !== 0x30) {
                throw new Error(`无效的帧类型: 0x${actualFrameType.toString(16)}`);
            }

            // 检查数据有效性
            if (!(data instanceof Uint8Array)) {
                throw new Error('无效的数据类型');
            }

            // 检查WebM帧头
            if (actualFrameType === 0x10) { // 初始化段
                // 检查EBML头
                if (data[0] !== 0x1A || data[1] !== 0x45 || data[2] !== 0xDF || data[3] !== 0xA3) {
                    throw new Error('初始化段缺少EBML头');
                }
            } else if (actualFrameType === 0x20) { // 关键帧
                // 检查Cluster头
                if (data[0] !== 0x1F || data[1] !== 0x43 || data[2] !== 0xB6 || data[3] !== 0x75) {
                    console.warn('关键帧缺少Cluster头，但继续处理');
                }
            }

            this.lastValidTimestamp = timestamp;
            this.lastFrameType = actualFrameType;
            this.consecutiveErrorCount = 0;
            return true;
        } catch (error) {
            console.error('帧验证失败:', error.message, {
                frameType: frameType * 0x10,
                timestamp,
                    size,
                firstBytes: Array.from(data.slice(0, 8))
                    .map(b => b.toString(16).padStart(2, '0'))
                    .join(' ')
            });
            return false;
        }
    }

    // 验证 WebM 头部
    validateWebMHeader(chunk) {
        if (chunk.length < 4) {
            console.warn('数据块太短，无法验证 WebM 头部');
            return false;
        }

        // WebM 头部的魔数
        const WEBM_HEADER = [0x1A, 0x45, 0xDF, 0xA3];
        
        // 检查前4个字节是否匹配 WebM 头部
        for (let i = 0; i < 4; i++) {
            if (chunk[i] !== WEBM_HEADER[i]) {
                console.warn('无效的 WebM 头部:', {
                    expected: WEBM_HEADER.map(b => b.toString(16).padStart(2, '0')).join(' '),
                    actual: Array.from(chunk.slice(0, 4)).map(b => b.toString(16).padStart(2, '0')).join(' ')
                });
                return false;
            }
        }

        return true;
    }

    checkPlaybackStatus() {
        if (!this.videoElement) return;

        const status = {
            readyState: this.videoElement.readyState,
            networkState: this.videoElement.networkState,
            paused: this.videoElement.paused,
            currentTime: this.videoElement.currentTime,
            duration: this.videoElement.duration,
            buffered: Array.from(this.videoElement.buffered).map(i => ({
                start: this.videoElement.buffered.start(i),
                end: this.videoElement.buffered.end(i)
            })),
            videoWidth: this.videoElement.videoWidth,
            videoHeight: this.videoElement.videoHeight
        };

        console.log('视频播放状态:', status);

        // 检查是否需要重新启动播放
        if (this.videoElement.paused && this.initSegmentAppended && this.sourceBuffer && 
            this.sourceBuffer.buffered.length > 0) {
            console.log('检测到视频暂停，尝试重新播放');
            this.videoElement.play().catch(e => {
                console.error('重新播放失败:', e);
            });
        }
    }

    parseFrameHeader(data) {
        if (!data || data.length < 4) {
            console.warn('无效的帧头数据');
            return null;
        }

        // 创建 DataView 来处理字节序
        const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
        
        // 第一个字节包含帧类型(高4位)和设备索引(低4位)
        const firstByte = view.getUint8(0);
        const frameType = (firstByte & 0xF0) >> 4;  // 获取高4位作为帧类型
        const deviceIndex = firstByte & 0x0F;       // 获取低4位作为设备索引

        // 读取时间戳（3字节，按照后端格式解析）
        const timestamp = (view.getUint8(3) << 12) |
                         (view.getUint8(2) << 4) |
                         (view.getUint8(1) & 0x0F);

        return {
            frameType,    // 0x10=初始化段, 0x20=关键帧, 0x30=普通帧
            deviceIndex,  // 1-15的设备索引
            timestamp,    // 毫秒时间戳
            payload: data.slice(4)  // 去除帧头后的实际数据
        };
    }

    validateWebMFrame(data) {
        if (!data || data.length < 8) {
            console.warn('帧数据太短');
            return false;
        }

        // 使用新的帧头解析方法
        const header = this.parseFrameHeader(data);
        
        // 记录帧头信息用于调试
        const frameInfo = {
            ...header,
            headerHex: Array.from(data.slice(0, 4))
                .map(b => b.toString(16).padStart(2, '0'))
                .join(' '),
            dataHex: Array.from(data.slice(4, 8))
                .map(b => b.toString(16).padStart(2, '0'))
                .join(' ')
        };

        console.log('验证帧数据:', frameInfo);

        // 对于初始化段，我们期望看到 EBML 头
        if (header.frameType === 0x10) { // 修改为0x10以匹配后端
            // 使用 DataView 读取 EBML 头
            const view = new DataView(data.buffer, data.byteOffset + 4, 4);
            const ebmlHeader = view.getUint32(0, false); // false 表示大端序
            const hasEBMLHeader = ebmlHeader === 0x1A45DFA3;
            if (!hasEBMLHeader) {
                console.warn('初始化段缺少 EBML 头');
                return false;
            }
            return true;
        }

        // 对于关键帧，我们期望看到 Cluster 头或者 EBML 头
        if (header.frameType === 0x20) { // 修改为0x20以匹配后端
            const view = new DataView(data.buffer, data.byteOffset + 4, 4);
            const headerValue = view.getUint32(0, false); // false 表示大端序
            const hasClusterHeader = headerValue === 0x1F43B675;
            const hasEBMLHeader = headerValue === 0x1A45DFA3;
            if (!hasClusterHeader && !hasEBMLHeader) {
                console.warn('关键帧缺少有效的头部标识');
                return false;
            }
            return true;
        }

        // 对于普通帧，只要确保有足够的数据即可
        if (header.frameType === 0x30) { // 修改为0x30以匹配后端
            return data.length > 4;
        }

        console.warn('未知的帧类型:', header.frameType);
        return false;
    }

    startKeyframeTimer() {
        if (this.keyframeTimer) {
            clearTimeout(this.keyframeTimer);
        }
        
        this.keyframeTimer = setTimeout(() => {
            if (!this.hasReceivedKeyframe) {
                console.warn('等待关键帧超时，可能需要重新请求视频流');
                // 这里可以触发重新请求的逻辑
                this.emit('keyframe-timeout');
            }
        }, this.keyframeTimeout);
    }

    // 添加事件发射器
    emit(event, data) {
        const handlers = this._eventHandlers && this._eventHandlers[event];
        if (handlers) {
            handlers.forEach(handler => handler(data));
        }
    }

    on(event, handler) {
        if (!this._eventHandlers) {
            this._eventHandlers = {};
        }
        if (!this._eventHandlers[event]) {
            this._eventHandlers[event] = [];
        }
        this._eventHandlers[event].push(handler);
    }

    off(event, handler) {
        const handlers = this._eventHandlers && this._eventHandlers[event];
        if (!handlers) {
            return;
        }
        if (handler) {
            const index = handlers.indexOf(handler);
            if (index !== -1) {
                handlers.splice(index, 1);
            }
        } else {
            this._eventHandlers[event] = [];
        }
    }

    async tryPlay() {
        if (!this.videoElement.paused) {
            return;
        }

        try {
            // 确保有足够的缓冲数据
            if (this.sourceBuffer.buffered.length > 0) {
                const bufferedStart = this.sourceBuffer.buffered.start(0);
                
                // 设置当前播放位置到缓冲区开始位置
                this.videoElement.currentTime = bufferedStart;

                console.log('尝试播放视频:', {
                    currentTime: this.videoElement.currentTime,
                    readyState: this.videoElement.readyState,
                    buffered: {
                        start: bufferedStart,
                        end: this.sourceBuffer.buffered.end(0)
                    }
                });

                // 尝试播放
                await this.videoElement.play();
                console.log('视频开始播放');
            }
        } catch (error) {
            console.warn('播放失败:', error);
            if (error.name === 'NotAllowedError') {
                // 添加点击事件监听器以响应用户交互
                this.videoElement.addEventListener('click', async () => {
                    try {
                        await this.videoElement.play();
                    } catch (playError) {
                        console.error('点击播放失败:', playError);
                    }
                }, { once: true });
            }
        }
    }
}

export class VideoPlayer {
    constructor() {
        this.players = new Map();  // 存储MSEPlayer实例
        this.audioPlayers = new Map(); // 存储音频预览实例
        this.deviceToTrackMap = new Map();  // 设备ID到轨道ID的映射
        this.deviceIndexToTrackMap = new Map();  // 设备索引到轨道ID的映射
        this.currentTracks = new Set();  // 当前活动的轨道
        this.isInitialized = false;
        this.previewSessions = new Map(); // 存储预览会话信息
        
        // 绑定方法
        this.handleStreamData = this.handleStreamData.bind(this);
        this.updateTrack = this.updateTrack.bind(this);
        this.removeTrack = this.removeTrack.bind(this);
        this.reset = this.reset.bind(this);
        this.startPreview = this.startPreview.bind(this);
        this.stopPreview = this.stopPreview.bind(this);
        this.startAudioPreview = this.startAudioPreview.bind(this);
        this.stopAudioPreview = this.stopAudioPreview.bind(this);
        
        console.log('VideoPlayer 实例已创建');
    }

    ensureVideoContainer() {
        let container = document.getElementById('videoContainer');
        if (!container) {
            container = document.createElement('div');
            container.id = 'videoContainer';
            container.style.display = 'flex';
            container.style.flexWrap = 'wrap';
            container.style.gap = '10px';
            container.style.marginTop = '20px';
            
            // 查找合适的位置插入容器
            const controlPanel = document.querySelector('.control-panel');
            if (controlPanel) {
                controlPanel.parentNode.insertBefore(container, controlPanel.nextSibling);
            } else {
                document.body.appendChild(container);
            }
            
            console.log('视频容器已创建');
        }
        return container;
    }

    async updateTrack(trackId, config) {
        try {
            console.log('更新轨道:', {
                trackId,
                config,
                currentTracks: Array.from(this.currentTracks),
                deviceIndexToTrackMap: Array.from(this.deviceIndexToTrackMap.entries())
            });

            // 确保视频容器存在
            const container = this.ensureVideoContainer();

            // 创建或获取video元素
            let videoElement = document.getElementById(`video-${trackId}`);
            if (!videoElement) {
                videoElement = document.createElement('video');
                videoElement.id = `video-${trackId}`;
                videoElement.autoplay = true;
                videoElement.playsInline = true;
                videoElement.muted = true;
                videoElement.style.width = '320px';
                videoElement.style.height = '240px';
                videoElement.style.backgroundColor = '#000';
                videoElement.style.borderRadius = '4px';
                videoElement.style.objectFit = 'cover';
                container.appendChild(videoElement);
            }

            // 创建或获取MSEPlayer实例
            let player = this.players.get(trackId);
            if (!player) {
                console.log('创建新的MSEPlayer实例:', trackId);
                player = new MSEPlayer(videoElement);
                this.players.set(trackId, player);
                console.log(`MSEPlayer 已创建: ${trackId}`);
            }

            // 确保MSEPlayer已初始化
            if (!player.isInitialized) {
                console.log(`开始初始化MSEPlayer: ${trackId}`);
                const initResult = await player.initialize();
                if (!initResult) {
                    throw new Error('MSEPlayer初始化失败');
                }
                player.isInitialized = true;
                console.log(`MSEPlayer 初始化完成: ${trackId}`, {
                    videoElement: {
                        readyState: videoElement.readyState,
                        error: videoElement.error,
                        networkState: videoElement.networkState
                    }
                });
            }

            let device = null;
            if (config.videoDevice) {
                device = config.videoDevice instanceof Device ? 
                    config.videoDevice : 
                    Device.fromJSON(config.videoDevice);
            }
            else if (config.audioDevice) {
                device = config.audioDevice instanceof Device ? 
                    config.audioDevice : 
                    Device.fromJSON(config.audioDevice);
            }

            // 获取设备索引
            const deviceIndex = device.index;
            if (!deviceIndex) {
                throw new Error('设备索引无效');
            }

            // 清理旧的映射
            for (const [oldDeviceId, oldTrackId] of this.deviceToTrackMap.entries()) {
                if (oldTrackId === trackId) {
                    this.deviceToTrackMap.delete(oldDeviceId);
                }
            }
            for (const [oldDeviceIndex, oldTrackId] of this.deviceIndexToTrackMap.entries()) {
                if (oldTrackId === trackId) {
                    this.deviceIndexToTrackMap.delete(oldDeviceIndex);
                }
            }

            // 更新设备映射
            console.log('设备映射更新前:', {
                deviceId: device.id,
                deviceIndex,
                trackId,
                deviceToTrackMap: Array.from(this.deviceToTrackMap.entries()),
                deviceIndexToTrackMap: Array.from(this.deviceIndexToTrackMap.entries())
            });
            
            this.deviceToTrackMap.set(device.id, trackId);
            this.deviceIndexToTrackMap.set(deviceIndex, trackId);
            this.currentTracks.add(trackId);

            console.log('设备映射更新后:', {
                deviceId: device.id,
                deviceIndex,
                trackId,
                deviceToTrackMap: Array.from(this.deviceToTrackMap.entries()),
                deviceIndexToTrackMap: Array.from(this.deviceIndexToTrackMap.entries())
            });

            return true;
        } catch (error) {
            console.error('更新轨道失败:', error);
            return false;
        }
    }

    handleStreamData(data) {
        if (!data || !data.data) {
            console.warn('无效的流数据');
            return;
        }

        const { frameType, deviceIndex, timestamp, data: frameData } = data;

        // 检查是否是音频预览会话
        let isAudioPreview = false;
        let audioSession = null;
        for (const [containerId, session] of this.audioPlayers.entries()) {
            if (session.device.index === deviceIndex) {
                isAudioPreview = true;
                audioSession = session;
                break;
            }
        }

        // 获取对应的MSEPlayer实例和日志前缀
        let player = null;
        let logPrefix = '';
        
        if (isAudioPreview && audioSession) {
            player = audioSession.player;
            logPrefix = '音频';
        } else {
            // 根据设备索引查找对应的轨道
            const trackId = this.deviceIndexToTrackMap.get(deviceIndex);
            if (!trackId) {
                console.warn('未找到对应的轨道:', {
                    deviceIndex,
                    currentTracks: Array.from(this.currentTracks),
                    deviceIndexToTrackMap: Array.from(this.deviceIndexToTrackMap.entries())
                });
                return;
            }

            player = this.players.get(trackId);
            logPrefix = '视频';
        }

        if (!player) {
            console.warn(`未找到对应的${logPrefix} MSEPlayer实例:`, {
                deviceIndex,
                players: Array.from(this.players.keys())
            });
            return;
        }

        // 记录处理信息
        console.log(`处理${logPrefix}流数据:`, {
            frameType,
            deviceIndex,
            timestamp,
            dataLength: frameData.byteLength,
            firstBytes: Array.from(new Uint8Array(frameData.slice(0, 16)))
                .map(b => b.toString(16).padStart(2, '0'))
                .join(' ')
        });

        // 统一调用MSEPlayer的handleStreamData方法
        try {
            player.handleStreamData({
                frameType,
                timestamp,
                data: frameData
            }).catch(error => {
                console.error(`处理${logPrefix}帧数据失败:`, error);
            });
        } catch (error) {
            console.error(`处理${logPrefix}帧数据失败:`, error);
        }
    }

    removeTrack(trackId) {
        const track = this.tracks.get(trackId);
        if (track) {
            // 清理设备映射
            if (track.deviceId) {
            const deviceIndex = this.getDeviceIndex(track.deviceId);
                this.videoDeviceToTrack.delete(deviceIndex);
            }
            if (track.audioDevice) {
                const audioDeviceIndex = this.getDeviceIndex(track.audioDevice.id);
                this.audioDeviceToTrack.delete(audioDeviceIndex);
            }

            // 清理MSE播放器
            track.msePlayer.reset();
            track.videoElement.remove();
            this.tracks.delete(trackId);
        }
    }

    reset() {
        // 清理所有轨道
        for (const [trackId, track] of this.tracks) {
            track.msePlayer.reset();
            track.videoElement.remove();
        }
        this.tracks.clear();
        this.videoDeviceToTrack.clear();
        this.audioDeviceToTrack.clear();
    }

    getTracksConfig() {
        const configs = {};
        for (const [trackId, track] of Object.entries(this.tracks)) {
            if (track.videoDevice && track.audioDevice) {
                configs[trackId] = {
                    deviceId: track.videoDevice.id,
                    deviceName: track.videoDevice.name,
                    devicePath: track.videoDevice.path,
                    type: track.videoDevice.type,
                    config: track.videoDevice.currentFormat,
                    audioDeviceId: track.audioDevice.id,
                    audioDeviceName: track.audioDevice.name,
                    audioDevicePath: track.audioDevice.path
                };
            }
        }
        return configs;
    }

    // 开始预览
    async startPreview(device, containerId) {
        if (!device || !containerId) {
            throw new Error('设备和容器ID不能为空');
        }

        console.log('开始预览:', {device, containerId});

        // 确保device是Device类的实例
        if (!(device instanceof Device)) {
            device = Device.fromJSON(device);
        }

        // 获取设备索引
        const deviceIndex = device.index;
        console.log('设备索引计算:', {
            deviceId: device.id,
            deviceIndex: deviceIndex,
            device: device
        });

        // 创建或获取预览容器
        let container = document.getElementById(containerId);
        if (!container) {
            console.warn(`找不到预览容器 ${containerId}，创建新容器`);
            container = document.createElement('div');
            container.id = containerId;
            container.className = 'preview-container';
            container.style.width = '100%';
            container.style.height = '240px';
            container.style.backgroundColor = '#000';
            container.style.borderRadius = '4px';
            container.style.overflow = 'hidden';
            container.style.position = 'relative';
        }

        // 创建视频元素
        const videoElement = document.createElement('video');
        videoElement.id = `video-${containerId}`;
        videoElement.autoplay = true;
        videoElement.playsInline = true;
        videoElement.muted = true;
        videoElement.style.width = '100%';
        videoElement.style.height = '100%';
        videoElement.style.objectFit = 'cover';
        container.appendChild(videoElement);

        // 创建MSE播放器
        const player = new MSEPlayer(videoElement);
        await player.initialize();
        
        // 记录设备索引映射
        // this.deviceIndexToTrackMap.set(deviceIndex, containerId);
        
        // 记录预览会话
        this.previewSessions.set(containerId, {
            device,
            player,
            containerId
        });

        console.log('预览映射更新:', {
            deviceId: device.id,
            deviceIndex,
            containerId,
            deviceIndexToTrackMap: Array.from(this.deviceIndexToTrackMap.entries())
        });

        return player;
    }

    // 停止预览
    stopPreview(deviceId) {
        // 查找对应的预览会话
        let sessionContainerId = null;
        for (const [containerId, session] of this.previewSessions.entries()) {
            if (session.device.id === deviceId) {
                sessionContainerId = containerId;
                break;
            }
        }

        if (!sessionContainerId) {
            console.warn('找不到预览会话:', deviceId);
            return;
        }

        const session = this.previewSessions.get(sessionContainerId);
        if (session) {
            const { player, containerId } = session;
            
            // 停止播放器
            if (player) {
                player.reset();
            }
            
            // 移除视频元素
            const container = document.getElementById(containerId);
            if (container) {
                const video = container.querySelector('video');
                if (video) {
                    video.remove();
                }
            }
            
            // 清理映射
            this.players.delete(containerId);
            this.deviceIndexToTrackMap.forEach((value, key) => {
                if (value === containerId) {
                    this.deviceIndexToTrackMap.delete(key);
                }
            });
            this.previewSessions.delete(containerId);

            console.log('预览已停止:', {
                deviceId,
                containerId,
                deviceIndexToTrackMap: Array.from(this.deviceIndexToTrackMap.entries())
            });
        }
    }

    // 开始音频预览
    async startAudioPreview(device, containerId) {
        if (!device || !containerId) {
            throw new Error('设备和容器ID不能为空');
        }

        console.log('开始音频预览:', {device, containerId});

        // 确保device是Device类的实例
        if (!(device instanceof Device)) {
            device = Device.fromJSON(device);
        }

        // 获取设备索引
        const deviceIndex = device.index;
        console.log('音频设备索引计算:', {
            deviceId: device.id,
            deviceIndex: deviceIndex,
            device: device
        });

        // 创建或获取预览容器
        let container = document.getElementById(containerId);
        if (!container) {
            console.warn(`找不到预览容器 ${containerId}，创建新容器`);
            container = document.createElement('div');
            container.id = containerId;
            container.className = 'preview-container';
            container.style.width = '100%';
            container.style.height = '50px'; // 音频预览容器高度较小
            container.style.backgroundColor = '#f0f0f0';
            container.style.borderRadius = '4px';
            container.style.overflow = 'hidden';
            container.style.position = 'relative';
        }

        // 创建音频元素
        const audioElement = document.createElement('audio');
        audioElement.id = `audio-${containerId}`;
        audioElement.autoplay = true;
        audioElement.controls = true; // 显示音频控制器
        audioElement.style.width = '100%';
        audioElement.style.height = '100%';
        container.appendChild(audioElement);

        // 创建MSE播放器
        const player = new MSEPlayer(audioElement);
        await player.initialize();
        
        // 记录设备索引映射
        this.deviceIndexToTrackMap.set(deviceIndex, containerId);
        
        // 记录预览会话
        this.audioPlayers.set(containerId, {
            device,
            player,
            containerId,
            element: audioElement
        });

        console.log('音频预览映射更新:', {
            deviceId: device.id,
            deviceIndex,
            containerId,
            deviceIndexToTrackMap: Array.from(this.deviceIndexToTrackMap.entries())
        });

        return player;
    }

    // 停止音频预览
    stopAudioPreview(deviceId) {
        // 查找对应的预览会话
        let sessionContainerId = null;
        for (const [containerId, session] of this.audioPlayers.entries()) {
            if (session.device.id === deviceId) {
                sessionContainerId = containerId;
                break;
            }
        }

        if (!sessionContainerId) {
            console.warn('找不到音频预览会话:', deviceId);
            return;
        }

        const session = this.audioPlayers.get(sessionContainerId);
        if (session) {
            const { player, containerId, element } = session;
            
            // 停止播放器
            if (player) {
                player.reset();
            }
            
            // 移除音频元素
            if (element) {
                element.remove();
            }
            
            // 清理映射
            this.audioPlayers.delete(containerId);
            this.deviceIndexToTrackMap.forEach((value, key) => {
                if (value === containerId) {
                    this.deviceIndexToTrackMap.delete(key);
                }
            });

            console.log('音频预览已停止:', {
                deviceId,
                containerId,
                deviceIndexToTrackMap: Array.from(this.deviceIndexToTrackMap.entries())
            });
        }
    }
} 