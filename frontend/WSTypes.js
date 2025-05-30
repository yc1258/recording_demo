// WebSocket消息类型定义
export const WSMessageType = {
    // 通用消息
    COMMON: {
        CONNECT: 'connect',
        DISCONNECT: 'disconnect',
        ERROR: 'error',
        READY: 'ready',
        CONFIG: 'config',
        STATUS: 'status'
    },
    
    // 设备控制
    DEVICE: {
        LIST: 'device_list',
        SELECT: 'select_device',
        FORMAT: 'select_format'
    },
    
    // 录制控制
    RECORDING: {
        START: 'start_recording',
        STOP: 'stop_recording',
        STATUS: 'recording_status',
        COMPLETE: 'recording_complete'
    },
    
    // 媒体流
    MEDIA: {
        FRAME: 'media_frame',
        CONFIG: 'media_config',
        KEYFRAME: 'keyframe'
    },
    
    DEVICES: 'devices',
    START_RECORDING: 'start_recording',
    STOP_RECORDING: 'stop_recording',
    ACK: 'ack',
    ERROR: 'error'
};

// 错误类型定义
export const WSErrorType = {
    CONNECTION_ERROR: 'connection_error',
    MESSAGE_ERROR: 'message_error',
    DEVICE_ERROR: 'device_error',
    RECORDING_ERROR: 'recording_error',
    MEDIA_ERROR: 'media_error'
};

// WebSocket状态类型
export const WSReadyState = {
    CONNECTING: 0,
    OPEN: 1,
    CLOSING: 2,
    CLOSED: 3
};

// WebSocket基础消息类
export class WSMessage {
    constructor(type, data = null) {
        this.type = type;
        this.data = data;
        this.messageId = Date.now().toString();
    }

    toJSON() {
        return {
            type: this.type,
            data: this.data,
            messageId: this.messageId
        };
    }
}

// 信令消息类
export class WSSignaling {
    constructor(type, payload = null) {
        this.type = type;
        this.payload = payload;
        this.messageId = Math.random().toString(36).substring(2);
    }

    toJSON() {
        return {
            type: this.type,
            payload: this.payload,
            messageId: this.messageId
        };
    }
}

// 设备配置消息类
export class WSDeviceConfig {
    constructor(videoDevice = null, audioDevice = null, format = null) {
        this.videoDevice = videoDevice;
        this.audioDevice = audioDevice;
        this.format = format;

        // 验证格式数据
        if (format) {
            console.log('设备配置 - 原始格式数据:', format);
            
            // 确保分辨率存在且有效
            if (!format.resolution || !format.resolution.width || !format.resolution.height) {
                console.error('无效的分辨率格式:', format.resolution);
                throw new Error('无效的分辨率格式');
            }

            // 确保帧率存在且有效
            if (!format.frameRate || !format.frameRate.num || !format.frameRate.den) {
                console.error('无效的帧率格式:', format.frameRate);
                throw new Error('无效的帧率格式');
            }

            // 确保像素格式存在
            if (!format.pixelFormat) {
                console.error('未指定像素格式');
                throw new Error('未指定像素格式');
            }

            console.log('设备配置 - 验证后的格式数据:', {
                resolution: format.resolution,
                frameRate: format.frameRate,
                pixelFormat: format.pixelFormat
            });
        }
    }

    toJSON() {
        const json = {
            videoDevice: this.videoDevice,
            audioDevice: this.audioDevice,
            format: this.format
        };
        console.log('设备配置 - 序列化数据:', json);
        return json;
    }
}

// 视频帧元数据类
export class WSMediaConfig {
    constructor(width, height, codec, sampleRate = null, channels = null) {
        this.width = width;
        this.height = height;
        this.codec = codec;
        this.sampleRate = sampleRate;
        this.channels = channels;
    }

    toJSON() {
        return {
            width: this.width,
            height: this.height,
            codec: this.codec,
            sampleRate: this.sampleRate,
            channels: this.channels
        };
    }
}

// 错误消息类
export class WSError {
    constructor(type, message, details = null) {
        this.type = type;
        this.message = message;
        this.details = details;
        this.timestamp = Date.now();
    }

    toJSON() {
        return {
            type: this.type,
            message: this.message,
            details: this.details,
            timestamp: this.timestamp
        };
    }
}

// 设备类型定义
export const DeviceType = {
    CAMERA: 'camera',
    SCREEN: 'screen',
    MICROPHONE: 'microphone',
    SYSTEM_AUDIO: 'system_audio'
};

// 分辨率类型
export class Resolution {
    constructor(width, height) {
        this.width = width;
        this.height = height;
    }

    toJSON() {
        return {
            width: this.width,
            height: this.height
        };
    }
}

// 帧率类型
export class FrameRate {
    constructor(num, den) {
        this.num = num;
        this.den = den;
    }

    toJSON() {
        return {
            num: this.num,
            den: this.den
        };
    }
}

// 视频格式类型
export class VideoFormat {
    constructor(resolution, frameRate, pixelFormat, offset = null) {
        this.resolution = resolution;
        this.frameRate = frameRate;
        this.pixelFormat = pixelFormat;
        this.offset = offset;
    }

    toJSON() {
        return {
            resolution: this.resolution,
            frameRate: this.frameRate,
            pixelFormat: this.pixelFormat,
            offset: this.offset
        };
    }

    static fromJSON(json) {
        if (!json) return null;
        return new VideoFormat(
            json.resolution ? new Resolution(json.resolution.width, json.resolution.height) : null,
            json.frameRate ? new FrameRate(json.frameRate.num, json.frameRate.den) : null,
            json.pixelFormat,
            json.offset
        );
    }
}

// 设备类型
export class Device {
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
        if (!json) return null;
        
        // 处理videoFormats，确保它是一个数组
        const videoFormats = Array.isArray(json.videoFormats) ? 
            json.videoFormats.map(format => VideoFormat.fromJSON(format)) : 
            [];

        // 创建Device实例
        const device = new Device(
            json.id,
            json.name,
            json.type,
            json.isAvailable !== undefined ? json.isAvailable : true,
            videoFormats,
            json.currentFormat ? VideoFormat.fromJSON(json.currentFormat) : null,
            json.path || ''
        );

        // 确保设备索引被计算
        device.index = device.calculateDeviceIndex(json.id);

        return device;
    }
} 