package handlers

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	ffmpegSampleRate = "16000"
	ffmpegChannels   = "1"
	ffmpegFormat     = "mp3"
)

func normalizeVoiceAudio(ctx context.Context, content []byte, mimeType, filename string) ([]byte, string, string, error) {
	if len(content) == 0 {
		return nil, "", "", fmt.Errorf("empty audio content")
	}

	lowerMime := strings.ToLower(strings.TrimSpace(mimeType))
	if isOpenAICompatibleAudio(lowerMime, filename) {
		return content, mimeType, filename, nil
	}

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin",
		"-y",
		"-i", "pipe:0",
		"-ac", ffmpegChannels,
		"-ar", ffmpegSampleRate,
		"-f", ffmpegFormat,
		"pipe:1",
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(content)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return nil, "", "", fmt.Errorf("ffmpeg failed: %w: %s", err, errMsg)
		}
		return nil, "", "", fmt.Errorf("ffmpeg failed: %w", err)
	}

	out := stdout.Bytes()
	if len(out) == 0 {
		return nil, "", "", fmt.Errorf("empty transcoded audio")
	}

	newMime := "audio/mpeg"
	newName := normalizeFilename(filename)
	return out, newMime, newName, nil
}

func normalizeFilename(filename string) string {
	if strings.TrimSpace(filename) == "" {
		return "voice.mp3"
	}
	lower := strings.ToLower(filename)
	if strings.HasSuffix(lower, ".mp3") {
		return filename
	}
	if ext := filepath.Ext(filename); ext != "" {
		return strings.TrimSuffix(filename, ext) + ".mp3"
	}
	return filename + ".mp3"
}

func isOpenAICompatibleAudio(mimeType, filename string) bool {
	if mimeType != "" {
		switch strings.ToLower(strings.TrimSpace(mimeType)) {
		case "audio/mpeg", "audio/mp3", "audio/mp4", "audio/mp4a-latm", "audio/x-m4a", "audio/m4a", "audio/wav", "audio/x-wav", "audio/webm":
			return true
		}
	}

	lowerName := strings.ToLower(strings.TrimSpace(filename))
	if lowerName == "" {
		return false
	}

	return strings.HasSuffix(lowerName, ".mp3") ||
		strings.HasSuffix(lowerName, ".mpeg") ||
		strings.HasSuffix(lowerName, ".mp4") ||
		strings.HasSuffix(lowerName, ".m4a") ||
		strings.HasSuffix(lowerName, ".wav") ||
		strings.HasSuffix(lowerName, ".webm")
}
