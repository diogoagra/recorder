#ifndef CONCAT_DARWIN_H
#define CONCAT_DARWIN_H

// concat_audio junta os arquivos de áudio de paths, em ordem, num MPEG-4 em
// out, copiando os pacotes de áudio como estão (sem recomprimir). Devolve 0 se
// deu certo; senão, preenche err.
int concat_audio(const char **paths, int count, const char *out, char *err, int errLen);

#endif
