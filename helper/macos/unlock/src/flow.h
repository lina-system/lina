#ifndef HU_FLOW_H
#define HU_FLOW_H
#include "runtime.h"
typedef struct {
    bool (*context)(hu_context *);
    bool (*active)(void);
    bool (*gui)(uid_t, unsigned, const hu_context *, hu_gui_response *);
    hu_result (*arm)(const hu_context *);
    bool (*consumed)(const hu_context *);
    void (*revoke)(void);
    void (*wait)(void);
} hu_flow_ops;
typedef struct {
    const char *result;
    bool screen_unlocked, consumed, ready;
    int legacy, data_protection, displays;
} hu_flow_result;
hu_flow_result hu_unlock_flow(uid_t uid, const hu_flow_ops *ops);
hu_flow_result hu_health_flow(uid_t uid, const hu_flow_ops *ops);
#endif
