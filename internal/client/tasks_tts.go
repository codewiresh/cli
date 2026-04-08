package client

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

type TaskSpeaker interface {
	Speak(ctx context.Context, text string) error
}

type TaskSpeakerOptions struct {
	Voice string
}

var (
	taskSpeakerLookPath = exec.LookPath
	taskSpeakerRun      = func(ctx context.Context, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		return cmd.Run()
	}
	taskSpeakerGOOS = runtime.GOOS
)

type taskSpeakerBackend struct {
	name      string
	buildArgs func(voice, text string) []string
}

type commandTaskSpeaker struct {
	command   string
	voice     string
	buildArgs func(voice, text string) []string
}

func NewTaskSpeaker(opts TaskSpeakerOptions) (TaskSpeaker, error) {
	candidates := taskSpeakerCandidates(taskSpeakerGOOS)
	for _, candidate := range candidates {
		path, err := taskSpeakerLookPath(candidate.name)
		if err != nil {
			continue
		}
		return &commandTaskSpeaker{
			command:   path,
			voice:     strings.TrimSpace(opts.Voice),
			buildArgs: candidate.buildArgs,
		}, nil
	}

	return nil, fmt.Errorf("no supported task speech backend found")
}

func (s *commandTaskSpeaker) Speak(ctx context.Context, text string) error {
	text = normalizeTaskSpeech(text)
	if text == "" {
		return nil
	}
	return taskSpeakerRun(ctx, s.command, s.buildArgs(s.voice, text)...)
}

func TaskSpeechText(ev TaskEvent) string {
	if ev.Type != "task.report" {
		return ""
	}

	summary := normalizeTaskSpeech(ev.Summary)
	if summary == "" {
		return ""
	}

	session := strings.TrimSpace(ev.SessionName)
	if session == "" && ev.SessionID != 0 {
		session = fmt.Sprintf("session %d", ev.SessionID)
	}
	node := strings.TrimSpace(ev.NodeName)
	state := strings.TrimSpace(ev.State)

	parts := make([]string, 0, 4)
	if node != "" {
		parts = append(parts, "Node "+node)
	}
	if session != "" {
		parts = append(parts, session)
	}
	if state != "" {
		parts = append(parts, state)
	}
	if len(parts) == 0 {
		return summary
	}
	return strings.Join(parts, ", ") + ". " + summary
}

func normalizeTaskSpeech(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func taskSpeakerCandidates(goos string) []taskSpeakerBackend {
	switch goos {
	case "darwin":
		return []taskSpeakerBackend{
			{name: "say", buildArgs: sayTaskSpeakerArgs},
			{name: "espeak-ng", buildArgs: espeakTaskSpeakerArgs},
			{name: "espeak", buildArgs: espeakTaskSpeakerArgs},
			{name: "spd-say", buildArgs: spdSayTaskSpeakerArgs},
		}
	case "windows":
		return []taskSpeakerBackend{
			{name: "pwsh", buildArgs: powershellTaskSpeakerArgs},
			{name: "powershell", buildArgs: powershellTaskSpeakerArgs},
		}
	default:
		return []taskSpeakerBackend{
			{name: "espeak-ng", buildArgs: espeakTaskSpeakerArgs},
			{name: "espeak", buildArgs: espeakTaskSpeakerArgs},
			{name: "spd-say", buildArgs: spdSayTaskSpeakerArgs},
			{name: "say", buildArgs: sayTaskSpeakerArgs},
		}
	}
}

func sayTaskSpeakerArgs(voice, text string) []string {
	args := make([]string, 0, 3)
	if voice != "" {
		args = append(args, "-v", voice)
	}
	args = append(args, text)
	return args
}

func espeakTaskSpeakerArgs(voice, text string) []string {
	args := make([]string, 0, 3)
	if voice != "" {
		args = append(args, "-v", voice)
	}
	args = append(args, text)
	return args
}

func spdSayTaskSpeakerArgs(voice, text string) []string {
	args := make([]string, 0, 3)
	if voice != "" {
		args = append(args, "-y", voice)
	}
	args = append(args, text)
	return args
}

func powershellTaskSpeakerArgs(voice, text string) []string {
	return []string{
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		`Add-Type -AssemblyName System.Speech; $s = New-Object System.Speech.Synthesis.SpeechSynthesizer; if ($args[0]) { $s.SelectVoice($args[0]) }; $s.Speak($args[1])`,
		voice,
		text,
	}
}
