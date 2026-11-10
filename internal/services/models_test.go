package services

import (
	"reflect"
	"testing"
)

func TestFilterModelsForEmbedding(t *testing.T) {
	models := []string{
		"BAAI/bge-m3",
		"BAAI/bge-reranker-v2-m3",
		"Qwen/Qwen3-Embedding-8B",
		"deepseek-ai/DeepSeek-V4.1-Flash",
		"Qwen/Qwen-Image",
		"FunAudioLLM/CosyVoice2-0.5B",
		"Pro/BAAI/bge-m3",
	}
	got := filterModels(models, "embed")
	want := []string{"BAAI/bge-m3", "Qwen/Qwen3-Embedding-8B", "Pro/BAAI/bge-m3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("embed filter = %v, want %v", got, want)
	}
}

func TestFilterModelsForRerank(t *testing.T) {
	models := []string{
		"BAAI/bge-m3",
		"BAAI/bge-reranker-v2-m3",
		"Qwen/Qwen3-Reranker-8B",
		"Qwen/Qwen3-Embedding-8B",
		"deepseek-ai/DeepSeek-V4.1-Flash",
	}
	got := filterModels(models, "rerank")
	want := []string{"BAAI/bge-reranker-v2-m3", "Qwen/Qwen3-Reranker-8B"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rerank filter = %v, want %v", got, want)
	}
}

func TestFilterModelsFallsBackWhenNothingMatches(t *testing.T) {
	models := []string{"custom-chat-model", "another-model"}
	got := filterModels(models, "embed")
	if !reflect.DeepEqual(got, models) {
		t.Fatalf("fallback should preserve list, got %v", got)
	}
}
