package main

import (
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// generateDiffHTML создает HTML с подсветкой изменений между двумя текстами
func generateDiffHTML(original, edited string) string {
	if original == edited {
		return escapeHTML(edited)
	}

	dmp := diffmatchpatch.New()

	// Создаем diff
	diffs := dmp.DiffMain(original, edited, false)

	// Оптимизируем для читаемости
	diffs = dmp.DiffCleanupSemantic(diffs)

	// Конвертируем в HTML
	var result strings.Builder

	for _, diff := range diffs {
		text := escapeHTML(diff.Text)

		switch diff.Type {
		case diffmatchpatch.DiffInsert:
			// Добавленный текст - подчеркнутый
			result.WriteString("<u>")
			result.WriteString(text)
			result.WriteString("</u>")
		case diffmatchpatch.DiffDelete:
			// Удаленный текст - зачеркнутый
			result.WriteString("<s>")
			result.WriteString(text)
			result.WriteString("</s>")
		case diffmatchpatch.DiffEqual:
			// Неизмененный текст
			result.WriteString(text)
		}
	}

	return result.String()
}

// generatePrettyDiff создает красивое представление изменений
func generatePrettyDiff(original, edited string) string {
	if original == edited {
		return escapeHTML(edited)
	}

	// Если один из текстов пустой
	if original == "" {
		return "<u>" + escapeHTML(edited) + "</u>"
	}
	if edited == "" {
		return "<s>" + escapeHTML(original) + "</s>"
	}

	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(original, edited, false)
	diffs = dmp.DiffCleanupSemantic(diffs)

	// Проверяем масштаб изменений
	totalChars := 0
	changedChars := 0

	for _, diff := range diffs {
		totalChars += len(diff.Text)
		if diff.Type != diffmatchpatch.DiffEqual {
			changedChars += len(diff.Text)
		}
	}

	changeRatio := float64(changedChars) / float64(totalChars)

	// Если изменено больше 70% текста, показываем до/после
	if changeRatio > 0.7 {
		return "<b>Было:</b>\n" + escapeHTML(original) + "\n\n<b>Стало:</b>\n" + escapeHTML(edited)
	}

	// Иначе показываем inline diff
	return generateDiffHTML(original, edited)
}
