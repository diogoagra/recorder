#import <AVFoundation/AVFoundation.h>
#include <stdio.h>
#include <string.h>

#include "concat_darwin.h"

// As versões síncronas (tracksWithMediaType:, timeRange) ainda funcionam e
// cobrem os macOS antigos; aqui o arquivo é local e a chamada já roda fora da
// thread principal.
#pragma clang diagnostic ignored "-Wdeprecated-declarations"

// Tempo máximo da exportação. É só cópia de pacotes: horas de áudio levam segundos.
static const int64_t kExportTimeoutSeconds = 600;

static void concat_error(char *err, int errLen, NSString *what, NSError *error) {
    if (err == NULL || errLen <= 0) {
        return;
    }
    NSString *msg = error ? [NSString stringWithFormat:@"%@: %@ (%ld)", what, error.localizedDescription, (long)error.code] : what;
    snprintf(err, errLen, "%s", msg.UTF8String);
}

int concat_audio(const char **paths, int count, const char *out, char *err, int errLen) {
    @autoreleasepool {
        AVMutableComposition *composition = [AVMutableComposition composition];
        AVMutableCompositionTrack *track = [composition addMutableTrackWithMediaType:AVMediaTypeAudio
                                                                    preferredTrackID:kCMPersistentTrackID_Invalid];
        CMTime cursor = kCMTimeZero;
        for (int i = 0; i < count; i++) {
            NSURL *url = [NSURL fileURLWithPath:[NSString stringWithUTF8String:paths[i]]];
            // Sem a duração precisa, a de um AAC é estimada e as partes se sobrepõem
            AVURLAsset *asset = [AVURLAsset URLAssetWithURL:url
                                                    options:@{AVURLAssetPreferPreciseDurationAndTimingKey : @YES}];
            AVAssetTrack *source = [[asset tracksWithMediaType:AVMediaTypeAudio] firstObject];
            if (source == nil) {
                concat_error(err, errLen, [NSString stringWithFormat:@"sem áudio em %@", url.lastPathComponent], nil);
                return 1;
            }
            NSError *error = nil;
            CMTimeRange range = source.timeRange;
            if (![track insertTimeRange:range ofTrack:source atTime:cursor error:&error]) {
                concat_error(err, errLen, [NSString stringWithFormat:@"não foi possível juntar %@", url.lastPathComponent], error);
                return 1;
            }
            cursor = CMTimeAdd(cursor, range.duration);
        }

        NSURL *outURL = [NSURL fileURLWithPath:[NSString stringWithUTF8String:out]];
        [[NSFileManager defaultManager] removeItemAtURL:outURL error:nil];
        // Passthrough: os pacotes AAC são copiados, sem decodificar e comprimir
        // de novo. Com o tipo m4a a exportação é recusada (-11838); o MPEG-4
        // guarda o mesmo áudio e toca igual.
        AVAssetExportSession *session = [[[AVAssetExportSession alloc] initWithAsset:composition
                                                                          presetName:AVAssetExportPresetPassthrough] autorelease];
        if (session == nil) {
            concat_error(err, errLen, @"exportação sem recompressão indisponível", nil);
            return 1;
        }
        session.outputURL = outURL;
        session.outputFileType = AVFileTypeMPEG4;
        // O índice (moov) no começo do arquivo: o player não precisa buscar o fim
        session.shouldOptimizeForNetworkUse = YES;

        dispatch_semaphore_t done = dispatch_semaphore_create(0);
        [session exportAsynchronouslyWithCompletionHandler:^{
          dispatch_semaphore_signal(done);
        }];
        long timedOut = dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, kExportTimeoutSeconds * NSEC_PER_SEC));
        if (timedOut != 0) {
            [session cancelExport];
            // O bloco de conclusão ainda vai rodar: espera para soltar o semáforo sem ninguém usando
            dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC));
            dispatch_release(done);
            concat_error(err, errLen, @"a exportação do áudio não terminou a tempo", nil);
            return 1;
        }
        dispatch_release(done);
        if (session.status != AVAssetExportSessionStatusCompleted) {
            concat_error(err, errLen, @"falha ao exportar o áudio", session.error);
            return 1;
        }
    }
    return 0;
}
