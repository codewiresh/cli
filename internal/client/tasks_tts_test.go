package client

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
)

func TestNewTaskSpeakerPrefersDarwinSay(t *testing.T) {
	origGOOS := taskSpeakerGOOS
	origLookPath := taskSpeakerLookPath
	origRun := taskSpeakerRun
	t.Cleanup(func() {
		taskSpeakerGOOS = origGOOS
		taskSpeakerLookPath = origLookPath
		taskSpeakerRun = origRun
	})

	taskSpeakerGOOS = "darwin"
	taskSpeakerLookPath = func(file string) (string, error) {
		if file == "say" {
			return "/usr/bin/say", nil
		}
		return "", exec.ErrNotFound
	}

	var gotName string
	var gotArgs []string
	taskSpeakerRun = func(ctx context.Context, name string, args ...string) error {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return nil
	}

	speaker, err := NewTaskSpeaker(TaskSpeakerOptions{Voice: "Samantha"})
	if err != nil {
		t.Fatalf("NewTaskSpeaker: %v", err)
	}
	if err := speaker.Speak(context.Background(), "ship the client"); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if gotName != "/usr/bin/say" {
		t.Fatalf("command = %q", gotName)
	}
	wantArgs := []string{"-v", "Samantha", "ship the client"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestNewTaskSpeakerFallsBackToEspeakNG(t *testing.T) {
	origGOOS := taskSpeakerGOOS
	origLookPath := taskSpeakerLookPath
	origRun := taskSpeakerRun
	t.Cleanup(func() {
		taskSpeakerGOOS = origGOOS
		taskSpeakerLookPath = origLookPath
		taskSpeakerRun = origRun
	})

	taskSpeakerGOOS = "linux"
	taskSpeakerLookPath = func(file string) (string, error) {
		switch file {
		case "espeak-ng":
			return "/usr/bin/espeak-ng", nil
		default:
			return "", exec.ErrNotFound
		}
	}

	var gotName string
	var gotArgs []string
	taskSpeakerRun = func(ctx context.Context, name string, args ...string) error {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return nil
	}

	speaker, err := NewTaskSpeaker(TaskSpeakerOptions{Voice: "en-us"})
	if err != nil {
		t.Fatalf("NewTaskSpeaker: %v", err)
	}
	if err := speaker.Speak(context.Background(), "  ship   the   client  "); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if gotName != "/usr/bin/espeak-ng" {
		t.Fatalf("command = %q", gotName)
	}
	wantArgs := []string{"-v", "en-us", "ship the client"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestNewTaskSpeakerUnavailable(t *testing.T) {
	origGOOS := taskSpeakerGOOS
	origLookPath := taskSpeakerLookPath
	t.Cleanup(func() {
		taskSpeakerGOOS = origGOOS
		taskSpeakerLookPath = origLookPath
	})

	taskSpeakerGOOS = "linux"
	taskSpeakerLookPath = func(file string) (string, error) {
		return "", exec.ErrNotFound
	}

	_, err := NewTaskSpeaker(TaskSpeakerOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTaskSpeechTextIncludesContext(t *testing.T) {
	got := TaskSpeechText(TaskEvent{
		Type:        "task.report",
		NodeName:    "builder-1",
		SessionName: "planner",
		State:       "working",
		Summary:     "  ship   the   client ",
	})
	want := "Node builder-1, planner, working. ship the client"
	if got != want {
		t.Fatalf("TaskSpeechText = %q, want %q", got, want)
	}
}

func TestTaskSpeechTextSkipsResets(t *testing.T) {
	if got := TaskSpeechText(TaskEvent{Type: "stream.reset", Summary: "ignored"}); got != "" {
		t.Fatalf("TaskSpeechText = %q", got)
	}
}

func TestTaskSpeakerRunnerErrorPropagates(t *testing.T) {
	origGOOS := taskSpeakerGOOS
	origLookPath := taskSpeakerLookPath
	origRun := taskSpeakerRun
	t.Cleanup(func() {
		taskSpeakerGOOS = origGOOS
		taskSpeakerLookPath = origLookPath
		taskSpeakerRun = origRun
	})

	taskSpeakerGOOS = "linux"
	taskSpeakerLookPath = func(file string) (string, error) {
		if file == "espeak" {
			return "/usr/bin/espeak", nil
		}
		return "", exec.ErrNotFound
	}
	taskSpeakerRun = func(ctx context.Context, name string, args ...string) error {
		return errors.New("boom")
	}

	speaker, err := NewTaskSpeaker(TaskSpeakerOptions{})
	if err != nil {
		t.Fatalf("NewTaskSpeaker: %v", err)
	}
	if err := speaker.Speak(context.Background(), "ship the client"); err == nil {
		t.Fatal("expected error")
	}
}
