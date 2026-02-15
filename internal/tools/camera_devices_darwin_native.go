//go:build darwin && cgo

package tools

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework AVFoundation

#import <Foundation/Foundation.h>
#import <AVFoundation/AVFoundation.h>
#include <stdlib.h>
#include <string.h>

static void set_error(char **err_out, NSString *message) {
    if (err_out == NULL) {
        return;
    }
    const char *utf8 = [message UTF8String];
    if (utf8 == NULL) {
        utf8 = "unknown error";
    }
    *err_out = strdup(utf8);
}

int aagent_list_cameras_darwin(char **json_out, char **err_out) {
    @autoreleasepool {
        if (json_out == NULL) {
            set_error(err_out, @"invalid output pointer");
            return 1;
        }

        NSMutableArray<AVCaptureDeviceType> *deviceTypes = [NSMutableArray arrayWithObject:AVCaptureDeviceTypeBuiltInWideAngleCamera];
#ifdef AVCaptureDeviceTypeContinuityCamera
        if (@available(macOS 14.0, *)) {
            [deviceTypes addObject:AVCaptureDeviceTypeContinuityCamera];
        }
#endif
#ifdef AVCaptureDeviceTypeExternal
        [deviceTypes addObject:AVCaptureDeviceTypeExternal];
#endif

        AVCaptureDeviceDiscoverySession *discovery =
            [AVCaptureDeviceDiscoverySession discoverySessionWithDeviceTypes:deviceTypes
                                                                   mediaType:AVMediaTypeVideo
                                                                    position:AVCaptureDevicePositionUnspecified];
        NSArray<AVCaptureDevice *> *devices = [discovery devices];

        NSMutableArray *result = [NSMutableArray arrayWithCapacity:devices.count];
        for (NSUInteger i = 0; i < devices.count; i++) {
            AVCaptureDevice *device = devices[i];
            NSString *name = device.localizedName ?: @"Camera";
            NSString *deviceID = device.uniqueID ?: @"";
            NSDictionary *entry = @{
                @"index": @((int)i + 1),
                @"name": name,
                @"id": deviceID
            };
            [result addObject:entry];
        }

        NSError *jsonErr = nil;
        NSData *jsonData = [NSJSONSerialization dataWithJSONObject:result options:0 error:&jsonErr];
        if (jsonData == nil || jsonErr != nil) {
            NSString *msg = jsonErr != nil ? jsonErr.localizedDescription : @"failed to encode camera list";
            set_error(err_out, msg);
            return 1;
        }

        NSString *jsonString = [[NSString alloc] initWithData:jsonData encoding:NSUTF8StringEncoding];
        if (jsonString == nil) {
            set_error(err_out, @"failed to decode camera list");
            return 1;
        }

        const char *utf8 = [jsonString UTF8String];
        if (utf8 == NULL) {
            set_error(err_out, @"failed to encode camera list utf8");
            return 1;
        }
        *json_out = strdup(utf8);
        return 0;
    }
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"unsafe"
)

func listCameraDevicesDarwin() ([]CameraDevice, error) {
	var cJSON *C.char
	var cErr *C.char
	rc := C.aagent_list_cameras_darwin(&cJSON, &cErr)
	if rc != 0 {
		defer func() {
			if cErr != nil {
				C.free(unsafe.Pointer(cErr))
			}
		}()
		if cErr != nil {
			return nil, fmt.Errorf("failed to list native camera devices: %s", C.GoString(cErr))
		}
		return nil, fmt.Errorf("failed to list native camera devices")
	}
	defer func() {
		if cJSON != nil {
			C.free(unsafe.Pointer(cJSON))
		}
	}()

	var devices []CameraDevice
	if err := json.Unmarshal([]byte(C.GoString(cJSON)), &devices); err != nil {
		return nil, fmt.Errorf("failed to decode native camera list: %w", err)
	}
	return devices, nil
}
