#import <Foundation/Foundation.h>
#import <CoreAudio/CoreAudio.h>
#import <CoreAudio/CATapDescription.h>
#import <CoreAudio/AudioHardwareTapping.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "systap_darwin.h"
#include "_cgo_export.h"

struct systap {
    uintptr_t handle;
    AudioObjectID tapID;
    AudioObjectID aggID;
    AudioDeviceIOProcID procID;
    int running;
};

static const AudioObjectPropertyAddress kRateAddr = {
    kAudioDevicePropertyNominalSampleRate,
    kAudioObjectPropertyScopeGlobal,
    kAudioObjectPropertyElementMain,
};

static void set_err(char *err, int errLen, const char *what, OSStatus status) {
    if (err == NULL || errLen <= 0) {
        return;
    }
    // OSStatus do Core Audio normalmente é um four-char code ('who?', '!dev'...)
    UInt32 code = CFSwapInt32HostToBig((UInt32)status);
    unsigned char cc[5] = {0};
    memcpy(cc, &code, 4);
    int printable = 1;
    for (int i = 0; i < 4; i++) {
        if (cc[i] < 32 || cc[i] > 126) {
            printable = 0;
        }
    }
    if (printable) {
        snprintf(err, errLen, "%s (OSStatus '%s')", what, cc);
    } else {
        snprintf(err, errLen, "%s (OSStatus %d)", what, (int)status);
    }
}

static OSStatus systap_ioproc(AudioObjectID device, const AudioTimeStamp *now,
                              const AudioBufferList *input, const AudioTimeStamp *inputTime,
                              AudioBufferList *output, const AudioTimeStamp *outputTime,
                              void *clientData) {
    systap *t = (systap *)clientData;
    if (input == NULL || input->mNumberBuffers == 0) {
        return noErr;
    }
    // O tap é mono e é a única entrada do aggregate, então só existe um buffer.
    const AudioBuffer *buf = &input->mBuffers[0];
    UInt32 channels = buf->mNumberChannels;
    if (buf->mData == NULL || channels == 0) {
        return noErr;
    }
    UInt32 frames = buf->mDataByteSize / (UInt32)(sizeof(float) * channels);
    if (frames > 0) {
        goSystemTapData(t->handle, (float *)buf->mData, (int)frames, (int)channels);
    }
    return noErr;
}

// Processos que tocam de novo o áudio de outros apps. O ARK, motor do
// SoundSource e do Audio Hijack, intercepta o som de cada app para aplicar
// volume e efeitos e o reproduz ~75ms depois. Como o tap pega todos os
// processos, sem excluí-los cada voz seria gravada duas vezes: o original do
// app e a cópia deles, o que soa como eco.
static NSString *const kReplayingBundleIDs[] = {
    @"com.rogueamoeba.arkaudiod",
};

// Processos fora da gravação: os que tocam de novo o áudio dos outros e o
// próprio Recorder. O som do Recorder é o inglês do Intérprete indo para o seu
// fone, e a sua fala já entra na gravação, em português, pelo microfone.
static NSArray<NSNumber *> *excluded_processes(void) API_AVAILABLE(macos(14.2)) {
    NSMutableArray<NSNumber *> *out = [NSMutableArray array];
    AudioObjectPropertyAddress listAddr = {
        kAudioHardwarePropertyProcessObjectList,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain,
    };
    UInt32 size = 0;
    if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &listAddr, 0, NULL, &size) != noErr || size == 0) {
        return out;
    }
    AudioObjectID *ids = malloc(size);
    if (ids == NULL) {
        return out;
    }
    if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &listAddr, 0, NULL, &size, ids) == noErr) {
        AudioObjectPropertyAddress bundleAddr = {
            kAudioProcessPropertyBundleID,
            kAudioObjectPropertyScopeGlobal,
            kAudioObjectPropertyElementMain,
        };
        for (UInt32 i = 0; i < size / sizeof(AudioObjectID); i++) {
            CFStringRef bundle = NULL;
            UInt32 bundleSize = sizeof(bundle);
            if (AudioObjectGetPropertyData(ids[i], &bundleAddr, 0, NULL, &bundleSize, &bundle) != noErr || bundle == NULL) {
                continue;
            }
            for (size_t j = 0; j < sizeof(kReplayingBundleIDs) / sizeof(kReplayingBundleIDs[0]); j++) {
                if ([(NSString *)bundle isEqualToString:kReplayingBundleIDs[j]]) {
                    [out addObject:@(ids[i])];
                }
            }
            CFRelease(bundle);
        }
    }
    free(ids);

    AudioObjectPropertyAddress pidAddr = {
        kAudioHardwarePropertyTranslatePIDToProcessObject,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain,
    };
    pid_t pid = getpid();
    AudioObjectID self = kAudioObjectUnknown;
    UInt32 selfSize = sizeof(self);
    if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &pidAddr, sizeof(pid), &pid, &selfSize, &self) == noErr &&
        self != kAudioObjectUnknown) {
        [out addObject:@(self)];
    }
    return out;
}

// Cria um tap global (mono, todos os processos menos os de excluded_processes)
// e um aggregate device privado que contém apenas esse tap.
// Não incluímos o dispositivo de saída como sub-device para não misturar as
// entradas dele (ex: mic de um headset).
static OSStatus create_tap_device(systap *t, const char **what) API_AVAILABLE(macos(14.2)) {
    @autoreleasepool {
        CATapDescription *desc = [[CATapDescription alloc] initMonoGlobalTapButExcludeProcesses:excluded_processes()];
        desc.name = @"Recorder System Audio";
        desc.UUID = [NSUUID UUID];
        desc.privateTap = YES;
        desc.muteBehavior = CATapUnmuted;

        OSStatus status = AudioHardwareCreateProcessTap(desc, &t->tapID);
        [desc release];
        if (status != noErr) {
            t->tapID = kAudioObjectUnknown;
            *what = "falha ao criar o tap de áudio do sistema";
            return status;
        }

        AudioObjectPropertyAddress uidAddr = {
            kAudioTapPropertyUID,
            kAudioObjectPropertyScopeGlobal,
            kAudioObjectPropertyElementMain,
        };
        CFStringRef tapUID = NULL;
        UInt32 size = sizeof(tapUID);
        status = AudioObjectGetPropertyData(t->tapID, &uidAddr, 0, NULL, &size, &tapUID);
        if (status != noErr || tapUID == NULL) {
            *what = "falha ao ler o UID do tap";
            return status != noErr ? status : kAudioHardwareUnspecifiedError;
        }

        NSDictionary *aggDesc = @{
            @kAudioAggregateDeviceNameKey: @"Recorder System Audio",
            @kAudioAggregateDeviceUIDKey: [[NSUUID UUID] UUIDString],
            @kAudioAggregateDeviceIsPrivateKey: @YES,
            @kAudioAggregateDeviceIsStackedKey: @NO,
            @kAudioAggregateDeviceTapListKey: @[ @{ @kAudioSubTapUIDKey: (NSString *)tapUID } ],
        };
        status = AudioHardwareCreateAggregateDevice((CFDictionaryRef)aggDesc, &t->aggID);
        CFRelease(tapUID);
        if (status != noErr) {
            t->aggID = kAudioObjectUnknown;
            *what = "falha ao criar o dispositivo agregado";
            return status;
        }
        return noErr;
    }
}

static systap *systap_create_impl(uintptr_t handle, double preferredRate, double *actualRate,
                                  char *err, int errLen) API_AVAILABLE(macos(14.2)) {
    systap *t = calloc(1, sizeof(systap));
    if (t == NULL) {
        snprintf(err, errLen, "sem memória");
        return NULL;
    }
    t->handle = handle;
    t->tapID = kAudioObjectUnknown;
    t->aggID = kAudioObjectUnknown;

    const char *what = NULL;
    OSStatus status = create_tap_device(t, &what);
    if (status != noErr) {
        set_err(err, errLen, what, status);
        goto fail;
    }

    // Pede ao Core Audio para já entregar na taxa desejada (ele faz o resample).
    // Se não aceitar, seguimos na taxa nativa e o lado Go converte.
    if (preferredRate > 0) {
        Float64 rate = preferredRate;
        AudioObjectSetPropertyData(t->aggID, &kRateAddr, 0, NULL, sizeof(rate), &rate);
    }
    Float64 rate = 0;
    UInt32 size = sizeof(rate);
    status = AudioObjectGetPropertyData(t->aggID, &kRateAddr, 0, NULL, &size, &rate);
    if (status != noErr || rate <= 0) {
        set_err(err, errLen, "falha ao ler a taxa de amostragem", status);
        goto fail;
    }
    *actualRate = rate;

    status = AudioDeviceCreateIOProcID(t->aggID, systap_ioproc, t, &t->procID);
    if (status != noErr) {
        t->procID = NULL;
        set_err(err, errLen, "falha ao registrar o callback de áudio", status);
        goto fail;
    }
    return t;

fail:
    systap_destroy(t);
    return NULL;
}

systap *systap_create(uintptr_t handle, double preferredRate, double *actualRate, char *err, int errLen) {
    if (@available(macOS 14.2, *)) {
        return systap_create_impl(handle, preferredRate, actualRate, err, errLen);
    }
    snprintf(err, errLen, "a captura do áudio do sistema requer macOS 14.2 ou superior");
    return NULL;
}

int systap_start(systap *t, char *err, int errLen) {
    OSStatus status = AudioDeviceStart(t->aggID, t->procID);
    if (status != noErr) {
        set_err(err, errLen, "falha ao iniciar a captura do áudio do sistema", status);
        return -1;
    }
    t->running = 1;
    return 0;
}

void systap_destroy(systap *t) {
    if (t == NULL) {
        return;
    }
    // AudioDeviceStop só retorna depois que o IOProc parou de ser chamado,
    // então depois disso o handle do lado Go pode ser liberado com segurança.
    if (t->running) {
        AudioDeviceStop(t->aggID, t->procID);
    }
    if (t->procID != NULL) {
        AudioDeviceDestroyIOProcID(t->aggID, t->procID);
    }
    if (t->aggID != kAudioObjectUnknown) {
        AudioHardwareDestroyAggregateDevice(t->aggID);
    }
    if (t->tapID != kAudioObjectUnknown) {
        if (@available(macOS 14.2, *)) {
            AudioHardwareDestroyProcessTap(t->tapID);
        }
    }
    free(t);
}
