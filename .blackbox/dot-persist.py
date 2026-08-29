#!/usr/bin/env python3
# Держит ОДНО постоянное DoT-соединение к ноде и шлёт запрос каждую секунду —
# как это делает телефон. Запусти на ЧИСТОЙ сети, затем в панели применяй конфиг
# и смотри, не появится ли "ОБРЫВ" в момент применения.
#
#   python3 dot-persist.py | tee dot-clean.log
#
# Ctrl+C — стоп. Пришли мне dot-clean.log. Если через apply идут только "ok" —
# сервер чист и на чистом пути; если "ОБРЫВ" ровно на применении — поймали.
import socket, ssl, struct, time
HOST, PORT, SNI = "212.67.10.15", 853, "dns.nolim.cloud"
def q(qid, name):
    h = struct.pack(">HHHHHH", qid, 0x0100, 1, 0, 0, 0)
    b = b""
    for p in name.split("."):
        b += bytes([len(p)]) + p.encode()
    return h + b + b"\x00" + struct.pack(">HH", 1, 1)
ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
raw = socket.create_connection((HOST, PORT), timeout=5)
tls = ctx.wrap_socket(raw, server_hostname=SNI)
print(time.strftime("%H:%M:%S"), "DoT подключён (одно постоянное соединение)", flush=True)
i = 0
while True:
    i += 1
    try:
        m = q(i % 65535, "gemini.google.com"); m = struct.pack(">H", len(m)) + m
        t0 = time.time(); tls.settimeout(5); tls.sendall(m)
        ln = tls.recv(2)
        if len(ln) < 2: raise Exception("соединение закрыто сервером")
        n = struct.unpack(">H", ln)[0]; data = b""
        while len(data) < n:
            c = tls.recv(n - len(data))
            if not c: raise Exception("обрыв в ответе")
            data += c
        print(time.strftime("%H:%M:%S"), f"q{i} ok {(time.time()-t0)*1000:.0f}ms", flush=True)
    except Exception as e:
        print(time.strftime("%H:%M:%S"), f"q{i} ОБРЫВ: {e}", flush=True); break
    time.sleep(1)
