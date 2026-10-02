#!/usr/bin/env python3
"""A local https server for the cli-play arena scenario (cpb play's fetch).

Usage: play-https.py <dir>
  <dir>/cert.pem and <dir>/key.pem: its certificate (cpb trusts it through
  SSL_CERT_FILE); it writes <dir>/port when it listens, and appends every
  request path to <dir>/requests.log.

Routes:
  /ok.cpb          a recipe
  /big.cpb         70 KiB, over play's 64 KiB cap
  /to-http         302 to the same server over http
  /chain/<n>       302 to /chain/<n-1>; /chain/0 is the recipe
  /slow.cpb        holds the connection for 40 s, past play's 30 s
  /mutate.cpb      the first request gets version 1, every later one version 2
"""
import http.server
import os
import ssl
import sys
import threading
import time

D = sys.argv[1]
LOG = os.path.join(D, "requests.log")
lock = threading.Lock()
served = {"mutate": 0}


def recipe(model):
    return ("-- title: Fetched\n-- description: Served over https.\n\n"
            "ALTER PLAYBOOK SET MODEL '%s';\n" % model).encode()


class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def send(self, code, body=b"", loc=None):
        self.send_response(code)
        if loc:
            self.send_header("Location", loc)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        with lock:
            with open(LOG, "a") as f:
                f.write(self.path + "\n")
        p = self.path
        port = self.server.server_address[1]
        if p == "/ok.cpb":
            self.send(200, recipe("claude-opus-5-5"))
        elif p == "/big.cpb":
            self.send(200, b"-- " + b"x" * (70 * 1024) + b"\n")
        elif p == "/to-http":
            self.send(302, loc="http://localhost:%d/ok.cpb" % port)
        elif p.startswith("/chain/"):
            n = int(p.rsplit("/", 1)[1])
            if n == 0:
                self.send(200, recipe("claude-opus-5-5"))
            else:
                self.send(302, loc="/chain/%d" % (n - 1))
        elif p == "/slow.cpb":
            time.sleep(40)
            self.send(200, recipe("slow"))
        elif p == "/mutate.cpb":
            with lock:
                served["mutate"] += 1
                first = served["mutate"] == 1
            self.send(200, recipe("claude-opus-5-5" if first else "claude-sonnet-5"))
        else:
            self.send(404, b"not found\n")


srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
srv.daemon_threads = True
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(os.path.join(D, "cert.pem"), os.path.join(D, "key.pem"))
srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
with open(os.path.join(D, "port.tmp"), "w") as f:
    f.write(str(srv.server_address[1]))
os.replace(os.path.join(D, "port.tmp"), os.path.join(D, "port"))
srv.serve_forever()
