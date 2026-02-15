//go:build darwin && cgo

package tools

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework AVFoundation -framework AppKit -framework CoreMedia -framework CoreVideo -framework CoreImage

#import <Foundation/Foundation.h>
#import <AVFoundation/AVFoundation.h>
#import <AppKit/AppKit.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <CoreImage/CoreImage.h>
#import <dispatch/dispatch.h>
#include <stdlib.h>
#include <string.h>

@interface AAgentFrameCaptureDelegate : NSObject <AVCaptureVideoDataOutputSampleBufferDelegate>
@property (atomic, strong) NSData *capturedData;
@property (atomic, strong) NSError *capturedError;
@property (atomic) BOOL didCapture;
@property (atomic) int frameCount;
@property (atomic) int minWarmupFrames;
@property (nonatomic) dispatch_semaphore_t semaphore;
@property (nonatomic) BOOL outputPNG;
@end

@implementation AAgentFrameCaptureDelegate
- (void)captureOutput:(AVCaptureOutput *)output
didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
       fromConnection:(AVCaptureConnection *)connection {
    (void)output;
    (void)connection;
    if (self.didCapture) {
        return;
    }

    self.frameCount += 1;
    if (self.frameCount < self.minWarmupFrames) {
        return;
    }
    self.didCapture = YES;

    CVImageBufferRef imageBuffer = CMSampleBufferGetImageBuffer(sampleBuffer);
    if (imageBuffer == NULL) {
        self.capturedError = [NSError errorWithDomain:@"aagent.camera"
                                                 code:500
                                             userInfo:@{NSLocalizedDescriptionKey: @"failed to get image buffer"}];
        dispatch_semaphore_signal(self.semaphore);
        return;
    }

    size_t width = CVPixelBufferGetWidth(imageBuffer);
    size_t height = CVPixelBufferGetHeight(imageBuffer);
    CIImage *ciImage = [CIImage imageWithCVImageBuffer:imageBuffer];
    if (ciImage == nil) {
        self.capturedError = [NSError errorWithDomain:@"aagent.camera"
                                                 code:500
                                             userInfo:@{NSLocalizedDescriptionKey: @"failed to create CIImage"}];
        dispatch_semaphore_signal(self.semaphore);
        return;
    }

    CIContext *ciContext = [CIContext contextWithOptions:nil];
    CGImageRef cgImage = [ciContext createCGImage:ciImage fromRect:CGRectMake(0, 0, width, height)];
    if (cgImage == NULL) {
        self.capturedError = [NSError errorWithDomain:@"aagent.camera"
                                                 code:500
                                             userInfo:@{NSLocalizedDescriptionKey: @"failed to create CGImage"}];
        dispatch_semaphore_signal(self.semaphore);
        return;
    }

    NSBitmapImageRep *bitmap = [[NSBitmapImageRep alloc] initWithCGImage:cgImage];
    CGImageRelease(cgImage);
    if (bitmap == nil) {
        self.capturedError = [NSError errorWithDomain:@"aagent.camera"
                                                 code:500
                                             userInfo:@{NSLocalizedDescriptionKey: @"failed to create bitmap image"}];
        dispatch_semaphore_signal(self.semaphore);
        return;
    }

    // Some webcams output near-black warm-up frames; skip them and keep waiting.
    unsigned char *pixels = [bitmap bitmapData];
    NSInteger bytesPerRow = [bitmap bytesPerRow];
    NSInteger widthPx = [bitmap pixelsWide];
    NSInteger heightPx = [bitmap pixelsHigh];
    if (pixels != NULL && bytesPerRow > 0 && widthPx > 0 && heightPx > 0) {
        const int samplesX = 6;
        const int samplesY = 4;
        double lumaSum = 0.0;
        int sampleCount = 0;
        for (int yi = 0; yi < samplesY; yi++) {
            NSInteger y = (NSInteger)((double)yi / (samplesY - 1) * (heightPx - 1));
            unsigned char *row = pixels + (y * bytesPerRow);
            for (int xi = 0; xi < samplesX; xi++) {
                NSInteger x = (NSInteger)((double)xi / (samplesX - 1) * (widthPx - 1));
                unsigned char *px = row + (x * 4); // BGRA
                double b = (double)px[0];
                double g = (double)px[1];
                double r = (double)px[2];
                lumaSum += (0.2126 * r) + (0.7152 * g) + (0.0722 * b);
                sampleCount += 1;
            }
        }
        double meanLuma = sampleCount > 0 ? (lumaSum / sampleCount) : 0.0;
        if (meanLuma < 8.0 && self.frameCount < 180) {
            return;
        }
    }

    NSBitmapImageFileType fileType = self.outputPNG ? NSBitmapImageFileTypePNG : NSBitmapImageFileTypeJPEG;
    NSDictionary *props = self.outputPNG ? @{} : @{NSImageCompressionFactor: @0.92};
    NSData *data = [bitmap representationUsingType:fileType properties:props];
    if (data == nil || data.length == 0) {
        self.capturedError = [NSError errorWithDomain:@"aagent.camera"
                                                 code:500
                                             userInfo:@{NSLocalizedDescriptionKey: @"failed to encode captured frame"}];
        dispatch_semaphore_signal(self.semaphore);
        return;
    }

    self.capturedData = data;
    dispatch_semaphore_signal(self.semaphore);
}
@end

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

int aagent_capture_photo_darwin(int camera_index, const char *output_path, const char *format, char **err_out) {
    @autoreleasepool {
        if (camera_index <= 0) {
            camera_index = 1;
        }
        if (output_path == NULL || format == NULL) {
            set_error(err_out, @"invalid capture arguments");
            return 1;
        }

        NSString *outputPath = [NSString stringWithUTF8String:output_path];
        NSString *formatStr = [[NSString stringWithUTF8String:format] lowercaseString];
        if (outputPath == nil || formatStr == nil) {
            set_error(err_out, @"invalid capture arguments encoding");
            return 1;
        }
        BOOL outputPNG = [formatStr isEqualToString:@"png"];
        if (!outputPNG && ![formatStr isEqualToString:@"jpg"] && ![formatStr isEqualToString:@"jpeg"]) {
            set_error(err_out, [NSString stringWithFormat:@"unsupported format: %@", formatStr]);
            return 1;
        }

        AVAuthorizationStatus auth = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeVideo];
        if (auth == AVAuthorizationStatusNotDetermined) {
            dispatch_semaphore_t authSem = dispatch_semaphore_create(0);
            __block BOOL granted = NO;
            [AVCaptureDevice requestAccessForMediaType:AVMediaTypeVideo completionHandler:^(BOOL ok) {
                granted = ok;
                dispatch_semaphore_signal(authSem);
            }];
            dispatch_semaphore_wait(authSem, dispatch_time(DISPATCH_TIME_NOW, (int64_t)(10 * NSEC_PER_SEC)));
            if (!granted) {
                set_error(err_out, @"camera access was denied");
                return 1;
            }
            auth = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeVideo];
        }
        if (auth != AVAuthorizationStatusAuthorized) {
            set_error(err_out, @"camera access is not authorized for this process");
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
        if (devices.count == 0) {
            set_error(err_out, @"no camera devices found");
            return 1;
        }
        if (camera_index > (int)devices.count) {
            set_error(err_out, [NSString stringWithFormat:@"camera_index out of range: %d (available: %lu)",
                                camera_index, (unsigned long)devices.count]);
            return 1;
        }

        AVCaptureDevice *device = devices[(NSUInteger)(camera_index - 1)];
        NSError *captureErr = nil;
        AVCaptureDeviceInput *input = [AVCaptureDeviceInput deviceInputWithDevice:device error:&captureErr];
        if (input == nil || captureErr != nil) {
            NSString *msg = captureErr != nil ? captureErr.localizedDescription : @"unable to create camera input";
            set_error(err_out, msg);
            return 1;
        }

        AVCaptureSession *session = [[AVCaptureSession alloc] init];
        [session beginConfiguration];
        if ([session canSetSessionPreset:AVCaptureSessionPresetPhoto]) {
            [session setSessionPreset:AVCaptureSessionPresetPhoto];
        }
        if (![session canAddInput:input]) {
            [session commitConfiguration];
            set_error(err_out, @"unable to add camera input");
            return 1;
        }
        [session addInput:input];

        AVCaptureVideoDataOutput *videoOutput = [[AVCaptureVideoDataOutput alloc] init];
        videoOutput.alwaysDiscardsLateVideoFrames = YES;
        NSDictionary *videoSettings = @{
            (id)kCVPixelBufferPixelFormatTypeKey: @(kCVPixelFormatType_32BGRA)
        };
        videoOutput.videoSettings = videoSettings;

        if (![session canAddOutput:videoOutput]) {
            [session commitConfiguration];
            set_error(err_out, @"unable to add video output");
            return 1;
        }
        [session addOutput:videoOutput];
        [session commitConfiguration];

        AAgentFrameCaptureDelegate *delegate = [[AAgentFrameCaptureDelegate alloc] init];
        delegate.semaphore = dispatch_semaphore_create(0);
        delegate.outputPNG = outputPNG;
        delegate.minWarmupFrames = 20;
        dispatch_queue_t captureQueue = dispatch_queue_create("com.gratheon.aagent.camera.capture", DISPATCH_QUEUE_SERIAL);
        [videoOutput setSampleBufferDelegate:delegate queue:captureQueue];

        [session startRunning];

        long semResult = dispatch_semaphore_wait(delegate.semaphore, dispatch_time(DISPATCH_TIME_NOW, (int64_t)(12 * NSEC_PER_SEC)));
        [session stopRunning];
        [videoOutput setSampleBufferDelegate:nil queue:NULL];

        if (semResult != 0) {
            set_error(err_out, @"camera capture timed out");
            return 1;
        }
        if (delegate.capturedError != nil) {
            set_error(err_out, delegate.capturedError.localizedDescription);
            return 1;
        }
        if (delegate.capturedData == nil || delegate.capturedData.length == 0) {
            set_error(err_out, @"no image data captured");
            return 1;
        }

        NSURL *url = [NSURL fileURLWithPath:outputPath];
        NSError *writeErr = nil;
        BOOL ok = [delegate.capturedData writeToURL:url options:NSDataWritingAtomic error:&writeErr];
        if (!ok) {
            NSString *msg = writeErr != nil ? writeErr.localizedDescription : @"failed to write output image";
            set_error(err_out, msg);
            return 1;
        }
        return 0;
    }
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func captureCameraPhotoDarwin(cameraIndex int, format string, outputPath string) error {
	cOutputPath := C.CString(outputPath)
	cFormat := C.CString(format)
	defer C.free(unsafe.Pointer(cOutputPath))
	defer C.free(unsafe.Pointer(cFormat))

	var cErr *C.char
	rc := C.aagent_capture_photo_darwin(C.int(cameraIndex), cOutputPath, cFormat, &cErr)
	if rc == 0 {
		return nil
	}
	defer func() {
		if cErr != nil {
			C.free(unsafe.Pointer(cErr))
		}
	}()
	if cErr != nil {
		return fmt.Errorf("native camera capture failed: %s", C.GoString(cErr))
	}
	return fmt.Errorf("native camera capture failed")
}
