#include "macos_context.h"
#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/IOKitLib.h>
#include <mach/mach_time.h>
#include <string.h>
#include <sys/sysctl.h>

static bool is_true(CFTypeRef value) {
    return value && CFGetTypeID(value) == CFBooleanGetTypeID() &&
           CFBooleanGetValue(value);
}

bool hu_macos_context(hu_context *out) {
    memset(out, 0, sizeof(*out));
    io_registry_entry_t root = IORegistryGetRootEntry(kIOMainPortDefault);
    if (!root) return false;
    CFMutableDictionaryRef properties = NULL;
    kern_return_t kr = IORegistryEntryCreateCFProperties(root, &properties, kCFAllocatorDefault, 0);
    IOObjectRelease(root);
    if (kr != KERN_SUCCESS || !properties) return false;
    CFTypeRef users = CFDictionaryGetValue(properties, CFSTR("IOConsoleUsers"));
    CFTypeRef locked = CFDictionaryGetValue(properties, CFSTR("IOConsoleLocked"));
    if (!users || !locked || CFGetTypeID(locked) != CFBooleanGetTypeID()) {
        CFRelease(properties); return false;
    }
    out->locked = is_true(locked);
    bool found = false, valid = true;
    if (CFGetTypeID(users) != CFArrayGetTypeID()) valid = false;
    if (valid) for (CFIndex i = 0; i < CFArrayGetCount(users); i++) {
        CFTypeRef entry = CFArrayGetValueAtIndex(users, i);
        if (!entry || CFGetTypeID(entry) != CFDictionaryGetTypeID()) continue;
        if (!is_true(CFDictionaryGetValue(entry, CFSTR("kCGSSessionOnConsoleKey"))))
            continue;
        if (found) { valid = false; break; }
        found = true;
        CFTypeRef uid = CFDictionaryGetValue(entry, CFSTR("kCGSSessionUserIDKey"));
        CFTypeRef session = CFDictionaryGetValue(entry, CFSTR("CGSSessionUniqueSessionUUID"));
        int64_t numeric = -1;
        if (!uid || CFGetTypeID(uid) != CFNumberGetTypeID() ||
            !CFNumberGetValue(uid, kCFNumberSInt64Type, &numeric) ||
            numeric <= 0 || numeric >= UINT32_MAX || !session ||
            CFGetTypeID(session) != CFStringGetTypeID() ||
            !CFStringGetCString(session, out->session, sizeof(out->session), kCFStringEncodingUTF8)) {
            valid = false; break;
        }
        out->uid = (uid_t)numeric;
        /* These registry keys are not a stable public unlock API. Absence of a
         * positive lock indication NEVER permits arming or consuming a grant. */
    }
    CFRelease(properties);
    if (!valid || !found) return false;
    size_t size = sizeof(out->boot);
    if (sysctlbyname("kern.bootsessionuuid", out->boot, &size, NULL, 0) ||
        size > sizeof(out->boot) || out->boot[36] != 0) return false;
    mach_timebase_info_data_t scale;
    if (mach_timebase_info(&scale) != KERN_SUCCESS || scale.denom == 0) return false;
    out->now_ns = (uint64_t)(((__uint128_t)mach_continuous_time() * scale.numer) / scale.denom);
    return true;
}
