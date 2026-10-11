package tui

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"

	"github.com/charmbracelet/lipgloss"
)

// onlineDot: czy urzadzenie (po ID w transporcie) jest teraz polaczone.
func (m Model) onlineDot(peer string) string {
	if m.lobby == nil || peer == "" {
		return " "
	}
	for _, mem := range m.lobby.Members {
		if mem.ID == peer && mem.Connected {
			return lipgloss.NewStyle().Foreground(cOK).Render("●")
		}
	}
	return sDim.Render("○")
}

// peerStatus: stan wersji uczestnika o danym ID w transporcie.
func (m Model) peerStatus(peer string) *store.PeerStatus {
	if m.st == nil {
		return nil
	}
	for i, ps := range m.st.Peers {
		if ps.Device.Peer == peer {
			return &m.st.Peers[i]
		}
	}
	return nil
}

func (m Model) viewLobby(w int) string {
	var b strings.Builder
	if m.net == nil {
		b.WriteString(sDim.Render("\n  Praca bez sieci (-offline) - lobby niedostepne.") + "\n")
		return b.String()
	}
	if m.lobbyErr != nil {
		b.WriteString(sWarnBox.Width(w-4).Render("Syncthing: "+m.lobbyErr.Error()) + "\n")
	}
	l := m.lobby
	if l == nil {
		return b.String() + sDim.Render("\n  wczytywanie...") + "\n" + m.viewInvite(w)
	}

	title := fmt.Sprintf("Lobby (%d/%d)", len(l.Members)+1, syncthing.MaxMembers)
	b.WriteString(sSection.Render(title) + sDim.Render("   twoj kod urzadzenia: ") + sBold.Render(syncthing.ShortID(l.Me)) + "\n")
	b.WriteString(fmt.Sprintf("  %s %-20s %s\n", lipgloss.NewStyle().Foreground(cOK).Render("●"), trunc(m.p.Me.Name, 20), sDim.Render("ty")))
	for _, mem := range l.Members {
		dot, conn := sDim.Render("○"), sDim.Render("offline")
		if mem.Connected {
			dot = lipgloss.NewStyle().Foreground(cOK).Render("●")
			conn = lipgloss.NewStyle().Foreground(cOK).Render("polaczony") + sDim.Render(" · "+viaLabel(mem.Via))
		}
		rel := sDim.Render("jeszcze bez wersji")
		if ps := m.peerStatus(mem.ID); ps != nil {
			rel = relStyle(ps.Relation).Render(ps.Relation.String())
		}
		b.WriteString(fmt.Sprintf("  %s %-20s %s %s\n", dot, trunc(mem.Name, 20), pad(conn, 30), rel))
	}
	if len(l.Members) == 0 {
		b.WriteString(sDim.Render("  nikt jeszcze nie dolaczyl - zapros kogos (i)") + "\n")
	}

	if len(l.Requests) > 0 {
		b.WriteString(sSection.Render("Prosby o dolaczenie") + "\n")
		for i, r := range l.Requests {
			line := fmt.Sprintf("%-20s kod %s  %s", trunc(r.Name, 20), sBold.Render(syncthing.ShortID(r.ID)), sDim.Render(ago(r.Time)))
			if i == m.reqCur {
				line = sKey.Render("› ") + line
			} else {
				line = "  " + line
			}
			b.WriteString(line + "\n")
		}
		hint := "  Przyjmij tylko, jesli kod zgadza sie z tym, ktory widzi u siebie zapraszany."
		if l.Full() {
			hint = fmt.Sprintf("  Lobby pelne (%d osob) - nie mozna przyjac nikogo wiecej.", syncthing.MaxMembers)
		}
		b.WriteString(lipgloss.NewStyle().Foreground(cWarn).Render(hint) + "\n")
	}

	if st := l.Sync; st != nil {
		b.WriteString(sSection.Render("Synchronizacja") + "\n")
		switch {
		case st.Error != "":
			b.WriteString(lipgloss.NewStyle().Foreground(cBad).Render("  blad: "+st.Error) + "\n")
		case st.NeedFiles > 0:
			b.WriteString(fmt.Sprintf("  pobieranie: %d plik(ow), %s\n", st.NeedFiles, humanBytes(st.NeedBytes)))
		case st.State == "idle":
			b.WriteString(sDim.Render("  wszystko przeslane") + "\n")
		default:
			b.WriteString(sDim.Render("  "+st.State) + "\n")
		}
	}

	return b.String() + m.viewInvite(w)
}

func (m Model) viewInvite(w int) string {
	var b strings.Builder
	if m.invite != "" {
		note := "Wyslij ten kod osobie, ktora chcesz zaprosic (np. na Discordzie)."
		if m.copied {
			note = "Kod skopiowany do schowka - wklej go osobie, ktora chcesz zaprosic."
		}
		body := []string{
			sBold.Render("Zaproszenie do sesji " + filepath.Base(m.p.Dir)),
			sDim.Render(note),
			"",
		}
		body = append(body, wrap(m.invite, max(20, w-8))...)
		body = append(body, "",
			sDim.Render("Ta osoba wpisuje abcollab > Dolacz do projektu i wkleja kod. Jej prosba pojawi sie"),
			sDim.Render("tutaj - przyjmij (a), gdy kod urzadzenia zgadza sie z tym, ktory ona widzi u siebie."),
			sDim.Render("Nie ma abcollab? Instalacja: github.com/c-airr/ableton-collab/releases")+"  "+sKey.Render("esc")+sDim.Render(" zamknij"))
		b.WriteString("\n" + sBox.Width(w-4).Render(strings.Join(body, "\n")) + "\n")
	}
	return b.String()
}

// pad: dopelnienie do szerokosci widocznej (kody kolorow nie zajmuja miejsca).
func pad(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func viaLabel(v string) string {
	switch v {
	case "LAN":
		return "siec lokalna"
	case "internet":
		return "bezposrednio"
	case "relay":
		return "przez relay"
	}
	return v
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case t.IsZero():
		return ""
	case d < time.Minute:
		return "przed chwila"
	case d < time.Hour:
		return fmt.Sprintf("%d min temu", int(d.Minutes()))
	}
	return t.Local().Format("2006-01-02 15:04")
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// wrap tnie dlugi ciag (kod) na linie; ParseInvite ignoruje biale znaki,
// wiec kod skopiowany z kilku linii terminala nadal dziala.
func wrap(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}

// clipboard: podmieniany w testach, zeby nie ruszac schowka uzytkownika.
var clipboard = copyToClipboard

// copyToClipboard: schowek systemowy przez narzedzia systemu (bez cgo).
func copyToClipboard(s string) error {
	var cmds [][]string
	switch runtime.GOOS {
	case "windows":
		cmds = [][]string{{"clip"}}
	case "darwin":
		cmds = [][]string{{"pbcopy"}}
	default:
		cmds = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	}
	for _, c := range cmds {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(s)
		if cmd.Run() == nil {
			return nil
		}
	}
	return errors.New("brak schowka")
}
