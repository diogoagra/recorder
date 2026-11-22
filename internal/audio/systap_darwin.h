#ifndef SYSTAP_DARWIN_H
#define SYSTAP_DARWIN_H

#include <stdint.h>

// systap captura o áudio de saída do sistema (tudo que toca nos alto-falantes)
// usando Core Audio Process Taps (macOS 14.2+).
typedef struct systap systap;

// systap_create cria o tap e o aggregate device privado que o expõe como
// entrada. Em actualRate retorna a taxa em que o áudio será entregue.
// Retorna NULL e preenche err em caso de falha.
systap *systap_create(uintptr_t handle, double preferredRate, double *actualRate, char *err, int errLen);
int systap_start(systap *t, char *err, int errLen);
void systap_destroy(systap *t);

#endif
