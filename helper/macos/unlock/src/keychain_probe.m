#import <Foundation/Foundation.h>
#import <Security/Security.h>
#import <LocalAuthentication/LocalAuthentication.h>
#include "runtime.h"

int hu_keychain_probe(bool data_protection) {
    @autoreleasepool {
        /* Do not inspect existing items or prompt for credentials. Only our
         * random, non-synchronizing temporary generic-password item is used. */
        /* Required for the legacy-keychain branch; no UI may appear even if
         * legacy Security ignores the newer LAContext interaction policy. */
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
        SecKeychainSetUserInteractionAllowed(false);
#pragma clang diagnostic pop
        LAContext *authentication = [[LAContext alloc] init];
        authentication.interactionNotAllowed = YES;
        NSString *name = [@"local.hermes.unlock.probe." stringByAppendingString:NSUUID.UUID.UUIDString];
        NSData *payload = [NSUUID.UUID.UUIDString dataUsingEncoding:NSUTF8StringEncoding];
        NSMutableDictionary *query = [@{
            (__bridge id)kSecClass: (__bridge id)kSecClassGenericPassword,
            (__bridge id)kSecAttrService: name,
            (__bridge id)kSecAttrAccount: @"ephemeral-probe",
            (__bridge id)kSecAttrSynchronizable: @NO,
            (__bridge id)kSecUseDataProtectionKeychain: @(data_protection),
            (__bridge id)kSecUseAuthenticationContext: authentication
        } mutableCopy];
        NSMutableDictionary *add = [query mutableCopy];
        add[(__bridge id)kSecValueData] = payload;
        if (data_protection) add[(__bridge id)kSecAttrAccessible] = (__bridge id)kSecAttrAccessibleWhenUnlocked;
        OSStatus status = SecItemAdd((__bridge CFDictionaryRef)add, NULL);
        if (status != errSecSuccess) return (int)status;
        query[(__bridge id)kSecReturnData] = @YES;
        CFTypeRef result = NULL;
        status = SecItemCopyMatching((__bridge CFDictionaryRef)query, &result);
        if (status == errSecSuccess && (!result || ![(__bridge NSData *)result isEqualToData:payload]))
            status = errSecDecode;
        if (result) CFRelease(result);
        [query removeObjectForKey:(__bridge id)kSecReturnData];
        OSStatus cleanup = SecItemDelete((__bridge CFDictionaryRef)query);
        return (int)(status == errSecSuccess ? cleanup : status);
    }
}
