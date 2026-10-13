# AbletonCollab - Remote Script abcollab (wspolna praca na zywo).
#
# Podsluchuje zmiany w otwartym secie (nuty i wlasciwosci klipow, mikser
# sciezek, tempo) i wysyla je do abcollab; zmiany innych nanosi bez
# przeladowania setu. Zmian w strukturze (nowe sciezki, urzadzenia) nie
# przenosi - zglasza je jako "structure" i abcollab robi to przez zapis setu.
#
# Protokol: TCP 127.0.0.1:9879, jedna linia JSON na wiadomosc. Pierwsza
# wiadomosc klienta: {"op": "hello", "token": ...} (token z token.txt obok).
# Instaluje i aktualizuje go abcollab - nie edytuj recznie.

import json
import os
import socket
import threading
import time
import traceback

try:
    import queue
except ImportError:  # pragma: no cover
    import Queue as queue

import Live
from _Framework.ControlSurface import ControlSurface

PORT = 9879
VERSION = 4
SESSION_KEY = "abcollab.session"
HERE = os.path.dirname(os.path.abspath(__file__))
DEBOUNCE = 0.3


def create_instance(c_instance):
    return AbletonCollab(c_instance)


def canon(obj):
    return json.dumps(obj, sort_keys=True, separators=(",", ":"))


def r(x):
    return round(float(x), 6)


def nkey(n):
    # nuta w jednej postaci (z JSON-a od abcollab 0 przychodzi jako int)
    return canon([int(n[0]), r(n[1]), r(n[2]), r(n[3]), bool(n[4]), r(n[5]), r(n[6]), r(n[7])])


def merge_notes(base, theirs, mine):
    # trojstronne scalenie nut (jak mergeNotes w abcollab): zmienione tylko
    # u nich - jak u nich, tylko u mnie - jak u mnie, po obu stronach - wiecej
    b, t, m, note = {}, {}, {}, {}
    for ns, cnt in ((base, b), (theirs, t), (mine, m)):
        for n in ns:
            k = nkey(n)
            cnt[k] = cnt.get(k, 0) + 1
            note[k] = n
    out = []
    for k, n in note.items():
        bc, tc, mc = b.get(k, 0), t.get(k, 0), m.get(k, 0)
        if mc == bc:
            want = tc
        elif tc == bc:
            want = mc
        else:
            want = max(mc, tc)
        out.extend([n] * want)
    return out


class Mismatch(Exception):
    pass


class AbletonCollab(ControlSurface):
    def __init__(self, c_instance):
        ControlSurface.__init__(self, c_instance)
        self._song = self.song()
        self._token = self._read_token()
        self._inbox = queue.Queue()
        self._outbox = queue.Queue()
        self._client = None
        self._authed = False
        self._listeners = []
        self._dirty = {}
        self._last = {}
        self._structure_at = 0
        self._rebind = False
        self._running = True
        try:
            self._bind_all()
        except Exception:
            self.log_message("AbletonCollab bind: " + traceback.format_exc())
        self._server = None
        threading.Thread(target=self._serve, daemon=True).start()
        threading.Thread(target=self._writer, daemon=True).start()
        self.show_message("AbletonCollab ready")

    def disconnect(self):
        self._running = False
        self._unbind_all()
        try:
            if self._server:
                self._server.close()
        except Exception:
            pass
        self._drop_client()
        ControlSurface.disconnect(self)

    def _session(self):
        # znacznik sesji zapisany w secie (Song.get_data) - rozpoznanie setu
        # niezalezne od nazw sciezek
        try:
            return str(self._song.get_data(SESSION_KEY, "") or "")
        except Exception:
            return ""

    def _read_token(self):
        try:
            with open(os.path.join(HERE, "token.txt")) as f:
                return f.read().strip()
        except Exception:
            return ""

    # --- nasluchiwanie zmian -------------------------------------------------------

    def _listen(self, subject, prop, cb):
        add = getattr(subject, "add_%s_listener" % prop, None)
        rem = getattr(subject, "remove_%s_listener" % prop, None)
        has = getattr(subject, "%s_has_listener" % prop, None)
        if add is None:
            return
        try:
            add(cb)
        except Exception:
            return

        def remove():
            try:
                if has is None or has(cb):
                    rem(cb)
            except Exception:
                pass

        self._listeners.append(remove)

    def _unbind_all(self):
        for remove in self._listeners:
            remove()
        self._listeners = []

    def _bind_all(self):
        self._unbind_all()
        s = self._song
        self._listen(s, "tempo", self._marker("tempo"))
        self._listen(s, "tracks", self._on_structure)
        self._listen(s, "scenes", self._on_structure)
        for ti, t in enumerate(s.tracks):
            self._bind_track(ti, t)

    def _marker(self, key):
        def mark():
            self._dirty[key] = time.time()
        return mark

    def _on_structure(self):
        self._structure_at = time.time()
        self._rebind = True

    def _bind_track(self, ti, t):
        mark = self._marker("mixer/%d" % ti)
        self._listen(t, "name", mark)
        self._listen(t, "mute", mark)
        md = t.mixer_device
        self._listen(md.volume, "value", mark)
        self._listen(md.panning, "value", mark)
        for snd in md.sends:
            self._listen(snd, "value", mark)
        self._listen(t, "devices", self._on_structure)
        self._listen(t, "arrangement_clips", self._on_structure)
        for si, cs in enumerate(t.clip_slots):
            key = "clip/%d/s%d" % (ti, si)
            self._listen(cs, "has_clip", self._slot_changed(key))
            if cs.has_clip:
                self._bind_clip(key, cs.clip)
        for c in self._arrangement(t):
            self._bind_clip("clip/%d/a%s" % (ti, r(c.start_time)), c)

    def _slot_changed(self, key):
        def changed():
            self._dirty[key] = time.time()
            self._rebind = True
        return changed

    def _bind_clip(self, key, c):
        mark = self._marker(key)
        for prop in ("name", "color", "looping", "loop_start", "loop_end"):
            self._listen(c, prop, mark)
        if c.is_midi_clip:
            self._listen(c, "notes", mark)

    def _arrangement(self, t):
        try:
            return list(t.arrangement_clips)
        except Exception:
            return []

    # --- odczyt stanu ----------------------------------------------------------------

    def _track(self, ti, name=None):
        tracks = self._song.tracks
        if ti >= len(tracks):
            raise Mismatch("no track %d" % ti)
        t = tracks[ti]
        if name is not None and t.name != name:
            raise Mismatch("track %d is %r, not %r" % (ti, t.name, name))
        return t

    def _clip_at(self, t, where):
        if where.startswith("s"):
            cs = t.clip_slots[int(where[1:])]
            return cs, (cs.clip if cs.has_clip else None)
        start = float(where[1:])
        for c in self._arrangement(t):
            if abs(c.start_time - start) < 1e-6:
                return None, c
        return None, None

    def _read(self, key):
        if key == "tempo":
            return {"bpm": r(self._song.tempo)}
        parts = key.split("/")
        t = self._track(int(parts[1]))
        if parts[0] == "mixer":
            md = t.mixer_device
            return {"track": t.name, "vol": r(md.volume.value), "pan": r(md.panning.value),
                    "mute": bool(t.mute), "sends": [r(s.value) for s in md.sends]}
        _, c = self._clip_at(t, parts[2])
        if c is None:
            return {"track": t.name, "empty": True}
        obj = {"track": t.name, "name": c.name, "color": int(c.color), "looping": bool(c.looping),
               "loop": [r(c.loop_start), r(c.loop_end)], "len": r(c.length), "midi": bool(c.is_midi_clip)}
        if c.is_midi_clip:
            obj["notes"] = sorted(self._notes(c))
        return obj

    def _notes(self, c):
        out = []
        for n in c.get_all_notes_extended():
            out.append([int(n.pitch), r(n.start_time), r(n.duration), r(n.velocity), bool(n.mute),
                        r(getattr(n, "probability", 1.0)), r(getattr(n, "velocity_deviation", 0.0)),
                        r(getattr(n, "release_velocity", 64.0))])
        return out

    def _keys(self):
        keys = ["tempo"]
        for ti, t in enumerate(self._song.tracks):
            keys.append("mixer/%d" % ti)
            for si, cs in enumerate(t.clip_slots):
                if cs.has_clip:
                    keys.append("clip/%d/s%d" % (ti, si))
            for c in self._arrangement(t):
                keys.append("clip/%d/a%s" % (ti, r(c.start_time)))
        return keys

    def _emit(self, key):
        try:
            obj = self._read(key)
        except Exception:
            return
        c = canon(obj)
        if self._last.get(key) == c:
            return
        self._last[key] = c
        self._send({"t": "change", "key": key, "obj": obj})

    # --- nanoszenie zmian innych ------------------------------------------------------

    def _apply(self, key, obj, base=None, win=True):
        # base: stan, na ktorym nadawca zrobil zmiane - wtedy nanosimy tylko
        # to, co zmienil (nuty scalane z tym, co jest teraz w Live: rownoczesna
        # praca kilku osob sie sumuje). Pole zmienione naraz tu i tam (inaczej)
        # dostaje wartosc nadawcy tylko, gdy win (mniejsze ID urzadzenia).
        cur = None
        if base is not None:
            try:
                cur = self._read(key)
            except Exception:
                cur = None

        def take(f):
            if f not in obj:
                return False
            if base is None:
                return True
            if f in base and canon(base[f]) == canon(obj[f]):
                return False  # nadawca tego nie zmienil
            if cur is not None and f in cur and f in base and canon(cur[f]) != canon(base[f]) \
                    and canon(cur[f]) != canon(obj[f]) and not win:
                return False  # zmienione tu i tam - zostaje moje
            return True

        s = self._song
        if key == "tempo":
            if take("bpm"):
                s.tempo = obj["bpm"]
            return
        parts = key.split("/")
        t = self._track(int(parts[1]), obj.get("track"))
        if parts[0] == "mixer":
            md = t.mixer_device
            if take("vol"):
                md.volume.value = self._clamp(md.volume, obj["vol"])
            if take("pan"):
                md.panning.value = self._clamp(md.panning, obj["pan"])
            if take("mute"):
                t.mute = bool(obj["mute"])
            if take("sends"):
                for snd, v in zip(md.sends, obj.get("sends", [])):
                    snd.value = self._clamp(snd, v)
            return
        cs, c = self._clip_at(t, parts[2])
        if obj.get("empty"):
            if base is not None and cur is not None and not cur.get("empty") and \
                    canon(sorted(map(nkey, cur.get("notes", [])))) != canon(sorted(map(nkey, base.get("notes", [])))):
                return  # usuniety tam, a zmieniony tutaj - zostaje
            if cs is not None and cs.has_clip:
                cs.delete_clip()
            elif cs is None and c is not None:
                raise Mismatch("arrangement clip removed - needs a reload")
            return
        if c is None:
            if cs is None:
                raise Mismatch("arrangement clip missing - needs a reload")
            if not obj.get("midi"):
                raise Mismatch("audio clip missing - needs a reload")
            cs.create_clip(obj.get("len") or 4.0)
            c = cs.clip
            base, cur = None, None  # nowy klip - wszystko od nadawcy
        if take("name"):
            c.name = obj.get("name", "")
        if take("color"):
            c.color = int(obj.get("color", c.color))
        if take("looping"):
            c.looping = bool(obj.get("looping", c.looping))
        if take("loop"):
            start, end = obj["loop"]
            if start >= c.loop_end:
                c.loop_end = end
                c.loop_start = start
            else:
                c.loop_start = start
                c.loop_end = end
        if c.is_midi_clip and "notes" in obj:
            target = obj["notes"]
            if base is not None and cur is not None and "notes" in cur:
                target = merge_notes(base.get("notes", []), target, cur["notes"])
            self._set_notes(c, target)

    def _clamp(self, param, v):
        return max(param.min, min(param.max, float(v)))

    def _set_notes(self, c, target):
        have = {}
        for n in c.get_all_notes_extended():
            k = nkey([n.pitch, n.start_time, n.duration, n.velocity, n.mute,
                      getattr(n, "probability", 1.0), getattr(n, "velocity_deviation", 0.0),
                      getattr(n, "release_velocity", 64.0)])
            have.setdefault(k, []).append(n.note_id)
        want = {}
        for n in target:
            want.setdefault(nkey(n), []).append(n)
        remove, add = [], []
        for k, ids in have.items():
            keep = len(want.get(k, []))
            remove.extend(ids[keep:])
        for k, ns in want.items():
            add.extend(ns[len(have.get(k, [])):])
        if remove:
            c.remove_notes_by_id(remove)
        if add:
            specs = []
            for n in add:
                specs.append(Live.Clip.MidiNoteSpecification(
                    pitch=int(n[0]), start_time=float(n[1]), duration=float(n[2]), velocity=float(n[3]),
                    mute=bool(n[4]), probability=float(n[5]), velocity_deviation=float(n[6]),
                    release_velocity=float(n[7])))
            c.add_new_notes(tuple(specs))

    # --- petla glowna (watek Live'a) ----------------------------------------------------

    def update_display(self):
        ControlSurface.update_display(self)
        try:
            self._drain_inbox()
            if self._rebind:
                self._rebind = False
                self._bind_all()
            now = time.time()
            for key, at in list(self._dirty.items()):
                if now - at >= DEBOUNCE:
                    self._dirty.pop(key, None)
                    if self._authed:
                        self._emit(key)
            if self._structure_at and now - self._structure_at >= 0.5:
                self._structure_at = 0
                if self._authed:
                    self._send({"t": "structure"})
        except Exception:
            self.log_message("AbletonCollab: " + traceback.format_exc())

    def _drain_inbox(self):
        while True:
            try:
                msg = self._inbox.get_nowait()
            except queue.Empty:
                return
            op = msg.get("op")
            if op == "hello":
                self._authed = bool(self._token) and msg.get("token") == self._token
                if not self._authed:
                    self._send({"t": "error", "err": "bad token"})
                    self._drop_client()
                    continue
                self._send({"t": "hello", "v": VERSION, "tracks": [t.name for t in self._song.tracks],
                            "session": self._session()})
            elif not self._authed:
                continue
            elif op == "state":
                items = {}
                for key in self._keys():
                    try:
                        obj = self._read(key)
                    except Exception:
                        continue
                    items[key] = obj
                    self._last[key] = canon(obj)
                self._send({"t": "state", "id": msg.get("id"), "tracks": [t.name for t in self._song.tracks],
                            "session": self._session(), "items": items})
            elif op == "mark":
                try:
                    self._song.set_data(SESSION_KEY, str(msg.get("session", "")))
                    self._send({"t": "marked", "id": msg.get("id"), "ok": True})
                except Exception as e:
                    self._send({"t": "marked", "id": msg.get("id"), "ok": False, "err": str(e)})
            elif op == "apply":
                key = msg.get("key")
                try:
                    self._apply(key, msg.get("obj") or {}, msg.get("base"), bool(msg.get("win", True)))
                    self._dirty.pop(key, None)
                    now = None
                    try:
                        now = self._read(key)
                        self._last[key] = canon(now)
                    except Exception:
                        pass
                    self._send({"t": "applied", "id": msg.get("id"), "key": key, "ok": True, "obj": now})
                except Exception as e:
                    self._send({"t": "applied", "id": msg.get("id"), "key": key, "ok": False, "err": str(e)})
            elif op == "ping":
                self._send({"t": "pong", "id": msg.get("id")})

    # --- gniazdo -----------------------------------------------------------------------------

    def _send(self, msg):
        self._outbox.put(msg)

    def _drop_client(self):
        c = self._client
        self._client = None
        self._authed = False
        if c is not None:
            try:
                c.close()
            except Exception:
                pass

    def _serve(self):
        try:
            srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            srv.bind(("127.0.0.1", PORT))
            srv.listen(1)
            self._server = srv
        except Exception as e:
            self.log_message("AbletonCollab: can't listen on %d: %s" % (PORT, e))
            return
        while self._running:
            try:
                conn, _ = srv.accept()
            except Exception:
                if not self._running:
                    return
                continue
            self._drop_client()
            self._client = conn
            threading.Thread(target=self._reader, args=(conn,), daemon=True).start()

    def _reader(self, conn):
        buf = b""
        try:
            while self._running:
                data = conn.recv(65536)
                if not data:
                    break
                buf += data
                while b"\n" in buf:
                    line, buf = buf.split(b"\n", 1)
                    if line.strip():
                        try:
                            self._inbox.put(json.loads(line.decode("utf-8")))
                        except Exception:
                            pass
        except Exception:
            pass
        if self._client is conn:
            self._client = None
            self._authed = False

    def _writer(self):
        while self._running:
            try:
                msg = self._outbox.get(timeout=0.5)
            except queue.Empty:
                continue
            c = self._client
            if c is None:
                continue
            try:
                c.sendall((json.dumps(msg) + "\n").encode("utf-8"))
            except Exception:
                if self._client is c:
                    self._client = None
                    self._authed = False
