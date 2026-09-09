import json, os, sys, socketserver, http.server, urllib.request, urllib.error
UP="https://openrouter.ai/api/v1"; TGT=os.environ.get("PROXY_MODEL","inception/mercury-2.5")
KEY=os.environ["OPENROUTER_API_KEY"]
LOG=open(os.path.join(os.path.dirname(os.path.abspath(__file__)),"px.log"),"a",buffering=1)
class H(http.server.BaseHTTPRequestHandler):
    protocol_version="HTTP/1.1"
    def log_message(self,f,*a): pass
    def do_POST(self):
        body=self.rfile.read(int(self.headers.get("Content-Length",0)))
        try:
            p=json.loads(body); LOG.write("REQ %s model=%s tools=%d\n"%(self.path,p.get("model"),len(p.get("tools") or [])))
            p["model"]=TGT; p.pop("metadata",None); body=json.dumps(p).encode()
        except Exception as e: LOG.write("REQ unparsed %s\n"%e)
        path=self.path[3:] if self.path.startswith("/v1/") else self.path
        r=urllib.request.Request(UP+path,data=body,method="POST")
        r.add_header("Content-Type","application/json"); r.add_header("Authorization","Bearer "+KEY)
        r.add_header("anthropic-version",self.headers.get("anthropic-version","2023-06-01"))
        try: up=urllib.request.urlopen(r,timeout=900)
        except urllib.error.HTTPError as e:
            d=e.read(); LOG.write("ERR %d %s\n"%(e.code,d[:150]))
            self.send_response(e.code); self.send_header("Content-Type","application/json")
            self.send_header("Content-Length",str(len(d))); self.end_headers(); self.wfile.write(d); return
        ct=up.headers.get("Content-Type","application/json")
        self.send_response(200); self.send_header("Content-Type",ct)
        if "event-stream" in ct:
            self.send_header("Transfer-Encoding","chunked"); self.end_headers()
            while True:
                c=up.read1(8192)
                if not c: break
                self.wfile.write(b"%x\r\n"%len(c)+c+b"\r\n"); self.wfile.flush()
            self.wfile.write(b"0\r\n\r\n")
        else:
            d=up.read(); self.send_header("Content-Length",str(len(d))); self.end_headers(); self.wfile.write(d)
class S(socketserver.ThreadingTCPServer): allow_reuse_address=True; daemon_threads=True
S(("127.0.0.1",int(sys.argv[1])),H).serve_forever()
