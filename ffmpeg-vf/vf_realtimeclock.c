// vf_realtimeclock.c
#include <stdio.h>
#include <string.h>
#ifdef _WIN32
#include <windows.h>
#else
#include <sys/time.h>
#endif

#include "libavutil/opt.h"
#include "libavutil/dict.h"
#include "libavfilter/avfilter.h"
#include "libavfilter/filters.h"
#include "libavfilter/video.h"
#include "libavutil/mem.h"

typedef struct RealTimeClockContext {
    const AVClass *class;
    FILE *logfile;
    char* logfilename;
    int frame_count;
} RealTimeClockContext;

// filter参数定义
#define OFFSET(x) offsetof(RealTimeClockContext, x)
#define FLAGS AV_OPT_FLAG_FILTERING_PARAM|AV_OPT_FLAG_VIDEO_PARAM
static const AVOption realtimeclock_options[] = {
    { "log", "log file name", OFFSET(logfilename), AV_OPT_TYPE_STRING, {.str="realtimeclock.log"}, 0, 0, FLAGS },
    { NULL }
};

AVFILTER_DEFINE_CLASS(realtimeclock);

// 获取当前系统时间（微秒级，UNIX时间戳.微秒），跨平台
static void get_wallclock_time(char *buf, int bufsize)
{
#ifdef _WIN32
    FILETIME ft;
    ULARGE_INTEGER uli;
    GetSystemTimeAsFileTime(&ft);
    uli.LowPart = ft.dwLowDateTime;
    uli.HighPart = ft.dwHighDateTime;
    // Windows FILETIME 是自 1601-01-01 的100纳秒数
    // 转为UNIX时间戳（秒和微秒）
    uint64_t t = uli.QuadPart - 116444736000000000ULL; // 1601->1970
    uint64_t us = t / 10;
    snprintf(buf, bufsize, "%llu.%06llu", (unsigned long long)(us / 1000000), (unsigned long long)(us % 1000000));
#else
    struct timeval tv;
    gettimeofday(&tv, NULL);
    snprintf(buf, bufsize, "%ld.%06ld", (long)tv.tv_sec, (long)tv.tv_usec);
#endif
}

static int filter_frame(AVFilterLink *inlink, AVFrame *frame)
{
    RealTimeClockContext *ctx = inlink->dst->priv;
    char buf[64];
    get_wallclock_time(buf, sizeof(buf));
    av_dict_set(&frame->metadata, "wallclock_time", buf, 0);

    // 写入到本地文件
    if (ctx->logfile) {
        fprintf(ctx->logfile, "%d,%s\n", ctx->frame_count++, buf);
        fflush(ctx->logfile);
    }

    // 直接转发到下一个 filter
    return ff_filter_frame(inlink->dst->outputs[0], frame);
}

static av_cold int init(AVFilterContext *ctx)
{
    RealTimeClockContext *rtc = ctx->priv;
    const char *filename;
    filename = (rtc->logfilename && rtc->logfilename[0]) ? rtc->logfilename : "realtimeclock.log";
    rtc->logfile = fopen(filename, "w");
    if (!rtc->logfile) {
        av_log(ctx, AV_LOG_ERROR, "无法打开日志文件: %s\n", filename);
        return AVERROR(errno);
    }
    return 0;
}

static av_cold void uninit(AVFilterContext *ctx)
{
    RealTimeClockContext *rtc = ctx->priv;
    if (rtc->logfile) {
        fclose(rtc->logfile);
        rtc->logfile = NULL;
    }
    av_freep(&rtc->logfilename);
}

// 定义一个自定义的 filterpad
static const AVFilterPad realtimeclock_filterpad[] = {
    {
        .name         = "default",
        .type         = AVMEDIA_TYPE_VIDEO,
        .filter_frame = filter_frame,
    }
};

const AVFilter ff_vf_realtimeclock = {
    .name        = "realtimeclock",
    .description = NULL_IF_CONFIG_SMALL("Inject wallclock time into each video frame's metadata and save to file."),
    .priv_size   = sizeof(RealTimeClockContext),
    .init        = init,
    .uninit      = uninit,
    .priv_class  = &realtimeclock_class,
    .flags       = AVFILTER_FLAG_SUPPORT_TIMELINE_GENERIC,
    FILTER_INPUTS(realtimeclock_filterpad),
    FILTER_OUTPUTS(ff_video_default_filterpad),
};
