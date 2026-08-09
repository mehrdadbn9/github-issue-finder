package main

import "testing"

// An ollama Gemini Pro bug written entirely in Chinese went out as an alert.
// Issues in a script that cannot be read here are not workable regardless of
// score, and the AI-Infra projects made these common.
func TestMostlyNonLatinDropsForeignScriptIssues(t *testing.T) {
	drop := []string{
		"gemini pro 模型加载失败，返回空响应，无法正常使用该模型进行推理",
		"运行时崩溃：内存分配错误导致进程退出，请尽快修复这个严重的问题",
		"モデルのロードに失敗しました。エラーメッセージが表示されます",
		"모델을 불러오지 못했습니다. 오류가 발생하여 실행할 수 없습니다",
		"Не удалось загрузить модель, произошла ошибка при инициализации",
	}
	for _, s := range drop {
		if !mostlyNonLatin(s) {
			t.Errorf("expected a foreign-script issue to be gated: %q", s)
		}
	}
}

// The gate is a ratio, not a "contains" check. English issues routinely quote a
// log line or a path with a few CJK characters in it, and dropping those would
// throw away good work.
func TestMostlyNonLatinKeepsEnglishIssues(t *testing.T) {
	keep := []string{
		"kubelet leaks cgroup mounts after pod eviction on a systemd host",
		"panic when the model name contains 中文 characters in the path",
		"Log output shows 错误 followed by the stack trace below, full details in English " +
			"describing how the reconciler retries and eventually gives up after ten attempts",
		"CUDA illegal memory access in ggml_cuda_flash_attn_ext_mma_f16_case<256, 256>",
		"",
	}
	for _, s := range keep {
		if mostlyNonLatin(s) {
			t.Errorf("false positive, an English issue would be dropped: %q", s)
		}
	}
}

// A handful of stray glyphs must not trip the gate on their own.
func TestMostlyNonLatinFloor(t *testing.T) {
	if mostlyNonLatin("build fails on 中文 path") {
		t.Error("a title with a few CJK characters was gated; the floor should keep it")
	}
}

// End to end through the scorer, the way the alert path sees it.
func TestLanguageGateDisqualifiesScore(t *testing.T) {
	got := scoreOf(t,
		"gemini pro 无法加载模型",
		"我在使用过程中遇到了这个问题，模型加载失败并且返回了空的响应内容，希望能够尽快得到修复",
		[]string{"bug"}, "someuser")
	if got != -1 {
		t.Errorf("a Chinese-language issue scored %.2f, want -1", got)
	}
}
