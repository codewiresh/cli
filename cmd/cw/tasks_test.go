package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/client"
)

type recordingTaskSpeaker struct {
	spoken []string
	err    error
}

func (s *recordingTaskSpeaker) Speak(ctx context.Context, text string) error {
	s.spoken = append(s.spoken, text)
	return s.err
}

func TestTasksCmdRejectsSpeakWithoutWatch(t *testing.T) {
	cmd := tasksCmd()
	cmd.SetArgs([]string{"--speak"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--speak requires --watch") {
		t.Fatalf("Execute error = %v", err)
	}
}

func TestRunTaskSpeechDeduplicatesReports(t *testing.T) {
	speaker := &recordingTaskSpeaker{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan client.TaskEvent, 4)
	done := make(chan struct{})
	go func() {
		runTaskSpeech(ctx, speaker, in)
		close(done)
	}()

	in <- client.TaskEvent{Type: "task.report", NodeName: "builder", SessionID: 7, State: "working", Summary: "ship client"}
	in <- client.TaskEvent{Type: "task.report", NodeName: "builder", SessionID: 7, State: "working", Summary: "ship client"}
	in <- client.TaskEvent{Type: "stream.reset", NetworkID: "project_alpha"}

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for speech loop")
	}

	if len(speaker.spoken) != 1 {
		t.Fatalf("spoken = %#v", speaker.spoken)
	}
	if speaker.spoken[0] != "Node builder, session 7, working. ship client" {
		t.Fatalf("spoken[0] = %q", speaker.spoken[0])
	}
}

func TestRunTaskSpeechWarnsOnce(t *testing.T) {
	speaker := &recordingTaskSpeaker{err: context.DeadlineExceeded}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan client.TaskEvent, 4)
	done := make(chan struct{})
	stderr := taskCmdStderr
	var buf bytes.Buffer
	taskCmdStderr = &buf
	t.Cleanup(func() { taskCmdStderr = stderr })

	go func() {
		runTaskSpeech(ctx, speaker, in)
		close(done)
	}()

	in <- client.TaskEvent{Type: "task.report", NodeName: "builder", SessionID: 7, State: "working", Summary: "ship client"}
	in <- client.TaskEvent{Type: "task.report", NodeName: "builder", SessionID: 8, State: "working", Summary: "ship docs"}

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for speech loop")
	}

	if got := strings.Count(buf.String(), "warning: task speech failed:"); got != 1 {
		t.Fatalf("warning count = %d, stderr = %q", got, buf.String())
	}
}
