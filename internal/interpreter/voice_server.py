"""Servidor de voz do Intérprete do Recorder: fala um texto em inglês com a voz
do usuário, clonada da amostra que ele gravou (Qwen3-TTS via MLX, só em Mac
com Apple Silicon). O app sobe este servidor e o encerra ao fechar: quando a
entrada padrão fecha (o app terminou, mesmo à força), o servidor sai.

Uso: python voice_server.py PORTA MODELO PASTA_DA_VOZ
  MODELO: o repositório no Hugging Face, com a revisão depois de um @
          ("organização/modelo@revisão"); sem revisão, vale a mais recente
  PASTA_DA_VOZ/amostra.wav e amostra.txt: a amostra e o texto exato dela
  RECORDER_VOICE_TOKEN (no ambiente): o segredo que os pedidos do app trazem

O servidor só escuta em 127.0.0.1, mas uma porta local é alcançada por qualquer
programa do computador e por páginas abertas no navegador, e o que ele faz é
falar qualquer texto com a voz do usuário. Por isso um POST só é atendido se
traz, no cabeçalho X-Recorder-Token, o segredo que o app sorteou ao subir o
servidor, se é JSON e se foi feito a 127.0.0.1 (um site que aponte o próprio
nome para 127.0.0.1 chega com outro Host).

GET  /health -> {"state": "baixando"|"carregando"|"pronto"|"erro", "progress": 0-100, "error": "..."}
POST /speak  {"text": "...", "ref_audio": "...", "ref_text": "..."} -> a fala em
             quadros, enviados enquanto ela é gerada. ref_audio/ref_text trocam a
             amostra por outro trecho (as opções da calibração); sem eles, vale a
             amostra
POST /warmup -> prepara o modelo para frases do tamanho das reais

Cada quadro do /speak tem 1 byte com o tipo, 4 com o tamanho (little-endian) e
o conteúdo:
  A: áudio, PCM int16 mono a 24 kHz
  F: fim da fala (sem conteúdo)
  E: a geração falhou; o conteúdo é o erro, em UTF-8
A resposta sem o quadro F ou E foi cortada (o servidor morreu no meio): o app
trata como erro, e não como a fala inteira.
"""
import hmac
import json
import os
import struct
import sys
import threading
import time
import traceback
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# Definidos em main(), pelos argumentos
MODEL = ""
VOICE_DIR = ""
RATE = 24000
# O segredo dos pedidos do app. Vem no ambiente, e não nos argumentos, que
# qualquer programa do computador enxerga. Sem ele, nenhum POST é atendido
TOKEN = os.environ.get("RECORDER_VOICE_TOKEN", "")
# Um pedido é uma frase e, na calibração, o caminho de um trecho
MAX_BODY = 64 * 1024

state = {"state": "carregando", "progress": 0, "error": ""}
model = None
generating = threading.Lock()  # uma fala de cada vez


def sample():
    wav = os.path.join(VOICE_DIR, "amostra.wav")
    txt = os.path.join(VOICE_DIR, "amostra.txt")
    if not (os.path.exists(wav) and os.path.exists(txt)):
        return None, None
    with open(txt, encoding="utf-8") as f:
        return wav, f.read().strip()


# O modelo imita a referência por inteiro, inclusive o ruído de fundo da
# gravação (chiado do microfone, sala): com a amostra gravada no notebook, a
# voz saía com o ruído só ~25 dB abaixo da fala, audível no retorno pelos
# alto-falantes (no Meet, a supressão de ruído dele escondia). Limpar a
# referência antes deixa a voz gerada com o ruído ~45 dB abaixo, e ela ainda
# fica mais parecida com a voz real.
_clean_cache = {}


def clean_reference(path):
    """Devolve uma cópia da referência sem o ruído de fundo e no volume de
    fala padrão (guardada em PASTA_DA_VOZ/.sem-ruido, uma por referência,
    refeita só quando ela muda)."""
    import hashlib
    import wave
    import numpy as np
    st = os.stat(path)
    key = (os.path.realpath(path), st.st_mtime_ns, st.st_size)
    cached = _clean_cache.get(key)
    if cached and os.path.exists(cached):
        return cached
    with wave.open(path) as w:
        rate = w.getframerate()
        x = np.frombuffer(w.readframes(w.getnframes()), dtype="<i2").astype(np.float32) / 32768
    clean = normalize(denoise(x), rate)
    out_dir = os.path.join(VOICE_DIR, ".sem-ruido")
    os.makedirs(out_dir, exist_ok=True)
    out = os.path.join(out_dir, hashlib.sha1(key[0].encode()).hexdigest()[:16] + ".wav")
    with wave.open(out, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes((np.clip(clean, -1, 1) * 32767).astype("<i2").tobytes())
    _clean_cache[key] = out
    return out


def normalize(x, rate, speech_db=-18.0, peak=0.89):
    """Deixa a fala (os 10% de trechos mais altos) em speech_db: o modelo
    imita também o volume da referência, e sem o ruído ela fica mais baixa."""
    import numpy as np
    win = rate * 30 // 1000
    if len(x) < win:
        return x
    levels = sorted(10 * np.log10(np.mean(x[i:i + win] ** 2) + 1e-12) for i in range(0, len(x) - win, win))
    gain = 10 ** ((speech_db - levels[len(levels) * 9 // 10]) / 20)
    gain = min(gain, peak / (np.max(np.abs(x)) + 1e-9))
    return (x * gain).astype(np.float32)


def denoise(x, n=1024, hop=256, strength=2.0, floor=0.08):
    """Redução de ruído espectral: o perfil do ruído vem dos 10% de trechos
    mais baixos da gravação (as pausas entre as palavras)."""
    import numpy as np
    if len(x) < 2 * n:
        return x
    win = np.hanning(n).astype(np.float32)
    pad = np.concatenate([np.zeros(n, np.float32), x, np.zeros(n, np.float32)])
    count = 1 + (len(pad) - n) // hop
    frames = np.stack([pad[i * hop:i * hop + n] * win for i in range(count)])
    spec = np.fft.rfft(frames, axis=1)
    mag = np.abs(spec)
    energy = (mag ** 2).sum(axis=1)
    noise = mag[energy <= np.percentile(energy, 10)].mean(axis=0)
    gain = np.maximum(1 - strength * noise / (mag + 1e-9), floor)
    # Suaviza o ganho no tempo para não sobrar "ruído musical"
    kernel = np.ones(3) / 3
    gain = np.apply_along_axis(lambda g: np.convolve(g, kernel, mode="same"), 0, gain)
    out_frames = np.fft.irfft(spec * gain, n=n, axis=1) * win
    out = np.zeros(len(pad))
    norm = np.zeros(len(pad))
    for i in range(count):
        out[i * hop:i * hop + n] += out_frames[i]
        norm[i * hop:i * hop + n] += win ** 2
    return (out / np.maximum(norm, 1e-6))[n:n + len(x)].astype(np.float32)


def generate(text, on_chunk, ref=None):
    import numpy as np
    wav, ref_text = ref or sample()
    if wav is None:
        raise RuntimeError("a amostra da voz ainda não foi gravada")
    wav = clean_reference(wav)
    with generating:
        for r in model.generate(text=text, ref_audio=wav, ref_text=ref_text, lang_code="english",
                                stream=True, streaming_interval=0.32):
            pcm = np.clip(np.asarray(r.audio, dtype=np.float32), -1, 1)
            on_chunk((pcm * 32767).astype("<i2").tobytes())


def warmup():
    # O MLX compila o modelo na primeira vez que vê cada tamanho de texto: sem
    # isto, a primeira frase de verdade demorava ~4 s para começar
    for text in ("Okay, thanks.",
                 "I don't have any news about the test that was sent to the team. I think he is following up on it."):
        generate(text, lambda _: None)


def download(model_id):
    """Baixa o modelo (uns 4 GB na primeira vez), com o andamento em state.

    model_id é "repositório@revisão". Com a revisão, o que é baixado é sempre o
    mesmo conteúdo, o que foi conferido: sem ela, valeria o que estivesse no
    repositório no dia, e quem pudesse escrever nele mudaria o que roda aqui.
    """
    from huggingface_hub import HfApi, snapshot_download
    repo, _, revision = model_id.partition("@")
    revision = revision or None
    try:
        return snapshot_download(repo, revision=revision, local_files_only=True)
    except Exception:
        pass
    state.update(state="baixando", progress=0)
    info = HfApi().model_info(repo, revision=revision, files_metadata=True)
    total = sum((s.size or 0) for s in info.siblings) or 1
    cache = os.path.join(os.environ.get("HF_HOME", os.path.expanduser("~/.cache/huggingface")), "hub",
                         "models--" + repo.replace("/", "--"))
    done = threading.Event()

    def watch():
        while not done.wait(1):
            size = 0
            for root, _, files in os.walk(cache):
                for f in files:
                    try:
                        size += os.path.getsize(os.path.join(root, f))
                    except OSError:
                        pass
            state["progress"] = min(99, int(size * 100 / total))

    threading.Thread(target=watch, daemon=True).start()
    try:
        return snapshot_download(repo, revision=revision)
    finally:
        done.set()


def load():
    global model
    try:
        path = download(MODEL)
        state.update(state="carregando", progress=100)
        from mlx_audio.tts.utils import load_model
        m = load_model(path)
        model = m
        if sample()[0] is not None:
            warmup()
        state.update(state="pronto")
    except Exception as e:  # o app mostra o erro nas configurações
        state.update(state="erro", error=str(e))


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def reply(self, code, payload):
        body = json.dumps(payload).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/health":
            return self.reply(200, state)
        self.reply(404, {"error": "não encontrado"})

    def refused(self):
        """Por que o pedido não é do app; None se é."""
        if (self.headers.get("Host") or "").rsplit(":", 1)[0] != "127.0.0.1":
            return "pedido feito a outro endereço"
        if (self.headers.get("Content-Type") or "").split(";")[0].strip().lower() != "application/json":
            return "o pedido precisa ser JSON"
        given = (self.headers.get("X-Recorder-Token") or "").encode()
        if not TOKEN or not hmac.compare_digest(given, TOKEN.encode()):
            return "pedido sem o segredo do app"
        return None

    def do_POST(self):
        why = self.refused()
        if why:
            return self.reply(403, {"error": why})
        try:
            length = int(self.headers.get("Content-Length") or 0)
            if length < 0 or length > MAX_BODY:
                return self.reply(413, {"error": "pedido grande demais"})
            body = json.loads(self.rfile.read(length) or b"{}")
        except ValueError:
            return self.reply(400, {"error": "pedido inválido"})
        if not isinstance(body, dict):
            return self.reply(400, {"error": "pedido inválido"})
        if state["state"] != "pronto" and not (state["state"] == "carregando" and model is not None):
            # O erro da carga, se houve; senão, ainda está carregando
            return self.reply(503, {**state, "error": state["error"] or "a voz ainda não está pronta"})
        if self.path == "/warmup":
            try:
                warmup()
                return self.reply(200, {"ok": True})
            except Exception as e:
                return self.reply(500, {"error": str(e)})
        if self.path != "/speak":
            return self.reply(404, {"error": "não encontrado"})
        text = (body.get("text") or "").strip()
        if not text:
            return self.reply(400, {"error": "texto vazio"})
        ref = None
        if body.get("ref_audio"):
            # Só trechos da pasta da voz (as opções da calibração)
            path = os.path.realpath(body["ref_audio"])
            if not path.startswith(os.path.realpath(VOICE_DIR) + os.sep) or not os.path.exists(path):
                return self.reply(400, {"error": "trecho de referência inválido"})
            ref = (path, (body.get("ref_text") or "").strip())
        elif sample()[0] is None:
            return self.reply(409, {"error": "a amostra da voz ainda não foi gravada"})
        # A resposta vai aos poucos, em quadros, até o de fim da fala
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("X-Sample-Rate", str(RATE))
        self.end_headers()

        def send(kind, payload=b""):
            self.wfile.write(frame(kind, payload))
            self.wfile.flush()

        try:
            generate(text, lambda pcm: send(b"A", pcm), ref)
            send(b"F")
        except (BrokenPipeError, ConnectionResetError):
            pass  # o app desistiu da fala
        except Exception as e:
            # O 200 já foi: o erro vai num quadro, para o app mostrar o motivo
            # em vez de receber meia frase como se fosse a frase inteira
            traceback.print_exc()
            sys.stderr.flush()
            try:
                send(b"E", (str(e) or type(e).__name__).encode("utf-8"))
            except OSError:
                pass


def frame(kind, payload=b""):
    """Monta um quadro da resposta do /speak."""
    return kind + struct.pack("<I", len(payload)) + payload


def exit_with_app():
    # O app segura a entrada padrão aberta; quando ela fecha, ele terminou
    sys.stdin.read()
    os._exit(0)


def serve(port):
    """Abre o servidor em 127.0.0.1 (porta 0: uma livre, nos testes)."""
    return ThreadingHTTPServer(("127.0.0.1", port), Handler)


def main():
    global MODEL, VOICE_DIR
    port, MODEL, VOICE_DIR = int(sys.argv[1]), sys.argv[2], sys.argv[3]
    threading.Thread(target=exit_with_app, daemon=True).start()
    server = serve(port)  # antes de carregar: com a porta ocupada, sai sem gastar memória
    threading.Thread(target=load, daemon=True).start()
    server.serve_forever()


if __name__ == "__main__":
    main()
