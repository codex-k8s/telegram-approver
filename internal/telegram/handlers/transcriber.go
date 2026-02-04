package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
)

// OpenAITranscriber uses OpenAI API for speech-to-text.
type OpenAITranscriber struct {
	client  openai.Client
	model   string
	timeout time.Duration
	log     *slog.Logger
}

// NewOpenAITranscriber initializes OpenAI transcription client.
func NewOpenAITranscriber(apiKey, model string, timeout time.Duration, log *slog.Logger) *OpenAITranscriber {
	client := openai.NewClient(option.WithAPIKey(apiKey))
	return &OpenAITranscriber{client: client, model: model, timeout: timeout, log: log}
}

// Transcribe converts audio to text.
func (t *OpenAITranscriber) Transcribe(ctx context.Context, reader io.Reader, filename, contentType, language string) (string, error) {
	if reader == nil {
		return "", errors.New("empty audio reader")
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", errors.New("empty audio content")
	}
	transcribeCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	if filename == "" {
		filename = "voice.mp3"
	}
	if contentType == "" {
		contentType = "audio/mpeg"
	}

	params := openai.AudioTranscriptionNewParams{
		File:  openai.File(bytes.NewReader(data), filename, contentType),
		Model: openai.AudioModel(t.model),
	}
	if language != "" {
		params.Language = param.NewOpt(language)
	}
	resp, err := t.client.Audio.Transcriptions.New(transcribeCtx, params)
	if err != nil {
		t.log.Error("OpenAI transcription failed", "error", err)
		return "", err
	}
	if resp == nil || resp.Text == "" {
		return "", errors.New("empty transcription result")
	}
	return resp.Text, nil
}
