# ableton collab

Work on one Ableton Live project with friends, each on your own computer. One person edits the melody, another the hi-hats. Changes to different things merge automatically. When two people change the same clip, you pick which version to keep, with a preview of both patterns.

The merge works on the project's **structure** (tracks, clips, notes, devices), not on text lines. Files travel peer-to-peer through a bundled [Syncthing](https://syncthing.net): no server, no account, and it works behind CGNAT / mobile internet.

## Install

**Windows** (PowerShell):

```powershell
irm https://raw.githubusercontent.com/c-airr/ableton-collab/main/install.ps1 | iex
```

**Linux / macOS**:

```sh
curl -fsSL https://raw.githubusercontent.com/c-airr/ableton-collab/main/install.sh | sh
```

Then open a new terminal and type `abcollab`. No admin rights needed. On first run it downloads Syncthing (checked by sha256).

## Use

- **Host**: pick a folder for the session. If there's no set in it yet, save one from Live into it (*File > Save Live Set As*). You get an invite code; send it to a friend.
- **Join**: paste the code. Use a new folder, or an existing one with your own copy of the project:
  - **Merge files** combines your set with the host's.
  - **Replace** deletes the folder's contents and downloads the host's files.
- The host accepts the request in **PEOPLE** after checking that the device code matches.
- After working in Live and saving, press **Save** to share your changes. Press **Get changes** to receive others' changes, then reopen the set in Live.

The whole session folder (samples, recordings…) is shared. Sets (`.als`) are never copied over each other: changes travel as versions and get merged.

Keys: `tab` moves between panels, arrows pick, `enter` presses. The letter next to a button is its shortcut. The mouse works too.

## Limitations

- Live doesn't reload the set on its own. After **Get changes**, reopen it. Don't save the old one, or you'll overwrite the others' changes.
- Syncing only runs while `abcollab` is open.
- Plugin state is merged as a whole: if two people tweak the same plugin, one version wins.
- Tested with Live 12 on Windows and Linux (Wine). macOS builds exist but are untested.

## Build

```sh
go build ./cmd/abcollab
go test ./...    # ABCOLLAB_SYNCTHING=<path to syncthing> also runs the real pairing test
```

Releases are built by GitHub Actions on a `v*` tag.

## License

MIT. Syncthing is MPL-2.0 and is downloaded unmodified from its official releases.
