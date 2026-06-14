/**
 * @file scheduler.c
 * @brief Channel Scheduler v2.3 — UART via command queue, I2C/SPI via bus layer
 */

#include "scheduler.h"
#include "config_mgr.h"
#include "msg_handler.h"
#include "bus.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/queue.h"
#include <string.h>

#define TAG "SCHEDULER"

#define CMD_QUEUE_DEPTH 16
#define CMD_TX_MAX 128
typedef enum { CMD_WRITE = 0, CMD_SAMPLE = 1 } cmd_type_t;
typedef struct {
    uint32_t request_id, channel_id;
    uint8_t  tx_data[CMD_TX_MAX];
    size_t   tx_len;
    uint32_t read_size, timeout_ms;
    cmd_type_t type;
} uart_cmd_t;
extern QueueHandle_t g_cmd_queue;
extern void scheduler_register_uart(uint32_t ch, uint8_t bt, const uint8_t *cfg, size_t cfglen);

typedef struct {
    config_channel_t config; bus_handle_t bus;
    uint32_t last_sequence; TickType_t last_sample_time; bool active;
} sched_channel_t;

static sched_channel_t s_channels[SCHED_MAX_CHANNELS];
static TaskHandle_t s_task_handle; static volatile bool s_running;
static void scheduler_task(void *p);

void scheduler_init(void) { memset(s_channels,0,sizeof(s_channels)); s_running=false; s_task_handle=NULL; }
void scheduler_start(void) { if(s_task_handle)return;
    const config_manifest_t *cfg=config_mgr_get_manifest();
    if(cfg&&cfg->applied){for(int i=0;i<cfg->channel_count&&i<MAX_CHANNELS;i++)scheduler_add_channel(&cfg->channels[i]);}
    s_running=true; xTaskCreatePinnedToCore(scheduler_task,"scheduler",SCHED_TASK_STACK,NULL,SCHED_TASK_PRIORITY,&s_task_handle,SCHED_TASK_CORE);}
void scheduler_stop(void) { s_running=false;
    if(s_task_handle){TaskHandle_t th=s_task_handle;s_task_handle=NULL;vTaskDelete(th);
        while(eTaskGetState(th)!=eDeleted)vTaskDelay(pdMS_TO_TICKS(10));}
    for(int i=0;i<SCHED_MAX_CHANNELS;i++){if(s_channels[i].active&&s_channels[i].bus){bus_close(s_channels[i].bus);s_channels[i].bus=NULL;}s_channels[i].active=false;}}
sched_err_t scheduler_add_channel(const config_channel_t *ch) { if(!ch)return SCHED_ERR_INVALID;
    int slot=-1; for(int i=0;i<SCHED_MAX_CHANNELS;i++){if(s_channels[i].active&&s_channels[i].config.id==ch->id)return SCHED_ERR_DUPLICATE;if(!s_channels[i].active&&slot<0)slot=i;}
    if(slot<0)return SCHED_ERR_FULL;
    memcpy(&s_channels[slot].config,ch,sizeof(config_channel_t));
    if(ch->bus_type==1){scheduler_register_uart(ch->id,ch->bus_type,ch->bus_config,ch->bus_config_len);s_channels[slot].bus=NULL;}
    else s_channels[slot].bus=bus_open(ch->bus_type,ch->bus_config,ch->bus_config_len);
    s_channels[slot].last_sequence=0;s_channels[slot].last_sample_time=0;s_channels[slot].active=true;
    return SCHED_OK;}
sched_err_t scheduler_remove_channel(uint32_t id) { for(int i=0;i<SCHED_MAX_CHANNELS;i++)if(s_channels[i].active&&s_channels[i].config.id==id){if(s_channels[i].bus)bus_close(s_channels[i].bus);s_channels[i].active=false;return SCHED_OK;}return SCHED_ERR_NOT_FOUND;}
bool scheduler_is_running(void){return s_running;}
uint8_t scheduler_get_channel_count(void){uint8_t c=0;for(int i=0;i<SCHED_MAX_CHANNELS;i++)if(s_channels[i].active)c++;return c;}

static void scheduler_task(void *p){(void)p;TickType_t wake=xTaskGetTickCount();
    while(s_running){vTaskDelayUntil(&wake,pdMS_TO_TICKS(10));TickType_t now=xTaskGetTickCount();
        for(int i=0;i<SCHED_MAX_CHANNELS;i++){if(!s_channels[i].active||!s_channels[i].config.enabled)continue;
            if(now-s_channels[i].last_sample_time<pdMS_TO_TICKS(s_channels[i].config.interval_ms))continue;
            s_channels[i].last_sample_time=now;
            if(s_channels[i].config.bus_type==1){uart_cmd_t cmd={.channel_id=s_channels[i].config.id,.tx_len=0,.read_size=0,.timeout_ms=30,.type=CMD_SAMPLE};
                if(s_channels[i].config.template_count>0){const config_template_t *t=config_mgr_get_template(s_channels[i].config.template_ids[0]);
                    if(t&&t->write_data_len>0){cmd.tx_len=t->write_data_len<CMD_TX_MAX?t->write_data_len:CMD_TX_MAX;memcpy(cmd.tx_data,t->write_data,cmd.tx_len);cmd.read_size=t->read_length;}}
                xQueueSend(g_cmd_queue,&cmd,0);}
            else if(s_channels[i].bus){uint8_t rx[256];size_t rl=0;bus_set_tx(s_channels[i].bus,NULL,0,0);
                if(bus_transact(s_channels[i].bus,rx,sizeof(rx),&rl)==BUS_OK&&rl>0){s_channels[i].last_sequence++;
                    uint64_t ts=(uint64_t)(xTaskGetTickCount()*1000/configTICK_RATE_HZ)*1000;
                    msg_handler_send_data_report(s_channels[i].config.id,ts,s_channels[i].last_sequence,rx,rl,0,0);}}}}
    vTaskDelete(NULL);}
