import socket, struct, sys, time
def rl(n):
    o=bytearray()
    while True:
        b=n%128; n//=128
        if n>0: b|=0x80
        o.append(b)
        if n==0: break
    return bytes(o)
def ms(x):
    b=x.encode(); return struct.pack(">H",len(b))+b
def conn(cid):
    vh=ms("MQTT")+bytes([4,0x02])+struct.pack(">H",60)
    body=vh+ms(cid)
    return bytes([0x10])+rl(len(body))+body
def pub(t,pl):
    body=ms(t)+pl
    return bytes([0x30])+rl(len(body))+body
def vi(v):
    o=bytearray()
    while v>0x7F:
        o.append((v&0x7F)|0x80); v>>=7
    o.append(v&0x7F); return bytes(o)
def tag(f,w): return vi((f<<3)|w)
def ev(f,v): return tag(f,0)+vi(v)
def eb(f,d): return tag(f,2)+vi(len(d))+d
def frame(ch,ts,seq,raw,edge):
    return bytes([0x03])+ev(1,ch)+ev(2,ts)+ev(3,seq)+eb(4,raw)+ev(7,edge)
h,p,topic,hexp,ch,edge = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4], int(sys.argv[5]), int(sys.argv[6])
pl=frame(ch, time.time_ns()//1_000_000, 1, bytes.fromhex(hexp), edge)
s=socket.create_connection((h,p),timeout=10)
s.sendall(conn("bbprobe-%d"%time.time_ns()))
if len(s.recv(4))<4: raise SystemExit("CONNACK short")
s.sendall(pub(topic,pl)); s.sendall(bytes([0xE0,0x00])); s.close()
print("PUBLISH_OK bytes=%d"%len(pl))
