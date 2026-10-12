"""Test skryptu AbletonCollab bez Live'a: atrapy modulow Live i _Framework.

Uruchom: python internal/live/script/test_script.py
(go test ./internal/live uruchamia go automatycznie, jesli jest python).
"""
import json
import os
import socket
import sys
import time
import types

HERE = os.path.dirname(os.path.abspath(__file__))


# --- atrapy API Live'a -----------------------------------------------------------------

class Observable(object):
    def __init__(self):
        self._ls = {}

    def _add(self, prop, cb):
        self._ls.setdefault(prop, []).append(cb)

    def _fire(self, prop):
        for cb in list(self._ls.get(prop, [])):
            cb()

    def __getattr__(self, name):
        for pre in ("add_", "remove_"):
            if name.startswith(pre) and name.endswith("_listener"):
                prop = name[len(pre):-len("_listener")]
                if pre == "add_":
                    return lambda cb: self._add(prop, cb)
                return lambda cb: self._ls.get(prop, []).remove(cb)
        if name.endswith("_has_listener"):
            prop = name[:-len("_has_listener")]
            return lambda cb: cb in self._ls.get(prop, [])
        raise AttributeError(name)


class Param(Observable):
    def __init__(self, v, lo=0.0, hi=1.0):
        Observable.__init__(self)
        self._v, self.min, self.max = v, lo, hi

    @property
    def value(self):
        return self._v

    @value.setter
    def value(self, v):
        self._v = v
        self._fire("value")


class Note(object):
    _next = 1

    def __init__(self, pitch, start_time, duration, velocity, mute=False, probability=1.0,
                 velocity_deviation=0.0, release_velocity=64.0):
        self.pitch, self.start_time, self.duration, self.velocity = pitch, start_time, duration, velocity
        self.mute, self.probability = mute, probability
        self.velocity_deviation, self.release_velocity = velocity_deviation, release_velocity
        self.note_id = Note._next
        Note._next += 1


class Clip(Observable):
    def __init__(self, length=4.0):
        Observable.__init__(self)
        self.name, self.color, self.looping = "", 5, True
        self.loop_start, self.loop_end, self.length = 0.0, length, length
        self.is_midi_clip = True
        self.notes = []

    def get_all_notes_extended(self):
        return list(self.notes)

    def remove_notes_by_id(self, ids):
        self.notes = [n for n in self.notes if n.note_id not in ids]
        self._fire("notes")

    def add_new_notes(self, specs):
        for s in specs:
            self.notes.append(Note(s.pitch, s.start_time, s.duration, s.velocity, s.mute, s.probability,
                                   s.velocity_deviation, s.release_velocity))
        self._fire("notes")


class ClipSlot(Observable):
    def __init__(self):
        Observable.__init__(self)
        self.clip = None

    @property
    def has_clip(self):
        return self.clip is not None

    def create_clip(self, length):
        self.clip = Clip(length)
        self._fire("has_clip")

    def delete_clip(self):
        self.clip = None
        self._fire("has_clip")


class Mixer(object):
    def __init__(self):
        self.volume, self.panning = Param(0.85), Param(0.0, -1.0, 1.0)
        self.sends = [Param(0.0)]


class Track(Observable):
    def __init__(self, name, slots=4):
        Observable.__init__(self)
        self.name, self._mute = name, False
        self.mixer_device = Mixer()
        self.clip_slots = [ClipSlot() for _ in range(slots)]
        self.arrangement_clips = []

    @property
    def mute(self):
        return self._mute

    @mute.setter
    def mute(self, v):
        self._mute = v
        self._fire("mute")


class Song(Observable):
    def __init__(self):
        Observable.__init__(self)
        self._tempo = 120.0
        self.tracks = [Track("Hats"), Track("Bass")]
        self.scenes = []
        self._data = {}

    def get_data(self, key, default):
        return self._data.get(key, default)

    def set_data(self, key, value):
        self._data[key] = value

    @property
    def tempo(self):
        return self._tempo

    @tempo.setter
    def tempo(self, v):
        self._tempo = v
        self._fire("tempo")


class Spec(object):
    def __init__(self, **kw):
        self.__dict__.update(kw)


SONG = Song()

live_mod = types.ModuleType("Live")
live_mod.Clip = types.SimpleNamespace(MidiNoteSpecification=Spec)
sys.modules["Live"] = live_mod
fw = types.ModuleType("_Framework")
cs_mod = types.ModuleType("_Framework.ControlSurface")


class ControlSurface(object):
    def __init__(self, c_instance):
        pass

    def song(self):
        return SONG

    def log_message(self, m):
        print("LOG", m)

    def show_message(self, m):
        pass

    def update_display(self):
        pass

    def disconnect(self):
        pass


cs_mod.ControlSurface = ControlSurface
sys.modules["_Framework"] = fw
sys.modules["_Framework.ControlSurface"] = cs_mod

# --- test ------------------------------------------------------------------------------------

sys.path.insert(0, HERE)
with open(os.path.join(HERE, "AbletonCollab", "token.txt"), "w") as f:
    f.write("tok")
try:
    import AbletonCollab as ac

    ac.PORT = int(os.environ.get("ABCOLLAB_TEST_PORT", "19879"))
    SONG.tracks[0].clip_slots[0].create_clip(4.0)
    SONG.tracks[0].clip_slots[0].clip.add_new_notes([Spec(pitch=60, start_time=0.0, duration=0.25, velocity=100,
                                                           mute=False, probability=1.0, velocity_deviation=0.0,
                                                           release_velocity=64.0)])
    surf = ac.create_instance(None)

    def pump(seconds):
        end = time.time() + seconds
        while time.time() < end:
            surf.update_display()
            time.sleep(0.02)

    pump(0.2)
    c = socket.create_connection(("127.0.0.1", ac.PORT), timeout=5)
    c.setblocking(False)
    buf = [b""]

    def send(m):
        c.setblocking(True)
        c.sendall((json.dumps(m) + "\n").encode())
        c.setblocking(False)

    def lines(seconds):
        end = time.time() + seconds
        while time.time() < end:
            pump(0.03)
            try:
                buf[0] += c.recv(65536)
            except (BlockingIOError, socket.timeout):
                pass
            while b"\n" in buf[0]:
                line, buf[0] = buf[0].split(b"\n", 1)
                yield json.loads(line)

    def recv(want):
        for m in lines(5):
            if m.get("t") == want:
                return m
        raise AssertionError("no %s" % want)

    send({"op": "hello", "token": "tok"})
    hello = recv("hello")
    assert hello["tracks"] == ["Hats", "Bass"] and hello["session"] == "", hello
    send({"op": "mark", "session": "SESJA1"})
    assert recv("marked")["ok"] and SONG.get_data("abcollab.session", "") == "SESJA1"
    send({"op": "state", "id": 2})
    st = recv("state")
    assert st["session"] == "SESJA1", st.get("session")
    assert st["items"]["clip/0/s0"]["notes"] == [[60, 0.0, 0.25, 100.0, False, 1.0, 0.0, 64.0]], st["items"]
    assert st["items"]["tempo"] == {"bpm": 120.0}

    # zmiana w Live -> wiadomosc "change"
    SONG.tracks[0].clip_slots[0].clip.add_new_notes([Spec(pitch=62, start_time=1.0, duration=0.25, velocity=90,
                                                           mute=False, probability=1.0, velocity_deviation=0.0,
                                                           release_velocity=64.0)])
    ch = recv("change")
    assert ch["key"] == "clip/0/s0" and len(ch["obj"]["notes"]) == 2, ch

    # zmiana od innych -> nanoszona; nuta 60 zostaje (to samo note_id), bez echa
    keep_id = [n.note_id for n in SONG.tracks[0].clip_slots[0].clip.notes if n.pitch == 60][0]
    target = dict(ch["obj"])
    target["notes"] = [[60, 0.0, 0.25, 100.0, False, 1.0, 0.0, 64.0], [67, 2.0, 0.5, 80.0, False, 1.0, 0.0, 64.0]]
    target["name"] = "hh2"
    send({"op": "apply", "key": "clip/0/s0", "obj": target, "id": 5})
    ap = recv("applied")
    assert ap["ok"], ap
    assert ap["obj"]["name"] == "hh2" and len(ap["obj"]["notes"]) == 2, ap  # stan po naniesieniu
    clip = SONG.tracks[0].clip_slots[0].clip
    assert sorted((n.pitch, n.start_time) for n in clip.notes) == [(60, 0.0), (67, 2.0)]
    assert [n.note_id for n in clip.notes if n.pitch == 60] == [keep_id], "unchanged note got a new id"
    assert clip.name == "hh2"
    for m in lines(0.8):
        assert m.get("key") != "clip/0/s0", "echo of an applied change: %r" % m

    # nowy klip w pustym slocie, mikser, tempo
    send({"op": "apply", "key": "clip/1/s2", "obj": {"track": "Bass", "name": "b", "color": 3, "looping": True,
                                                       "loop": [0.0, 8.0], "len": 8.0, "midi": True,
                                                       "notes": [[36, 0.0, 1.0, 110.0, False, 1.0, 0.0, 64.0]]}, "id": 6})
    assert recv("applied")["ok"]
    assert SONG.tracks[1].clip_slots[2].has_clip and SONG.tracks[1].clip_slots[2].clip.loop_end == 8.0
    send({"op": "apply", "key": "mixer/1", "obj": {"track": "Bass", "vol": 0.5, "pan": -0.2, "mute": True, "sends": [0.3]}, "id": 7})
    assert recv("applied")["ok"]
    assert SONG.tracks[1].mixer_device.volume.value == 0.5 and SONG.tracks[1].mute
    send({"op": "apply", "key": "tempo", "obj": {"bpm": 128.0}, "id": 8})
    assert recv("applied")["ok"] and SONG.tempo == 128.0

    # zla sciezka (inny set) -> blad, nic nie zmienione
    send({"op": "apply", "key": "mixer/0", "obj": {"track": "Drums", "vol": 0.1, "pan": 0, "mute": False, "sends": []}, "id": 9})
    bad = recv("applied")
    assert not bad["ok"] and "Hats" in bad["err"], bad
    assert SONG.tracks[0].mixer_device.volume.value == 0.85

    # zly token
    c2 = socket.create_connection(("127.0.0.1", ac.PORT), timeout=5)
    c2.sendall(b'{"op":"hello","token":"zly"}\n')
    pump(0.3)
    surf.disconnect()
    print("SCRIPT OK")
finally:
    os.remove(os.path.join(HERE, "AbletonCollab", "token.txt"))
