package notify

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/linxin2429/bili_notify/media"
	"github.com/linxin2429/bili_notify/model"
)

func classifyFiles(message Message, dataDir string, minimum, maximum int64) (Message, []model.DeliveryFile) {
	if len(message.Files) == 0 {
		return message, nil
	}
	ready := make([]model.DeliveryFile, 0, len(message.Files))
	skipped := make([]string, 0)
	for _, item := range message.Files {
		item.Name = safeAttachmentName(item.Name)
		reason := ""
		actualSize := item.Size
		if item.LocalizeError != "" {
			reason = "本地化失败：" + item.LocalizeError
		} else if item.LocalPath == "" || dataDir == "" {
			reason = "未保存到本地"
		} else {
			file, size, detected, err := media.OpenFile(dataDir, item.LocalPath)
			if err != nil {
				reason = "本地文件不可用"
			} else {
				_ = file.Close()
				actualSize = size
				item.Size = size
				if item.MIME == "" {
					item.MIME = detected
				}
				if size < minimum {
					reason = fmt.Sprintf("文件过小（渠道要求至少 %s）", formatBytes(minimum))
				} else if maximum > 0 && size > maximum {
					reason = fmt.Sprintf("超过渠道上限 %s", formatBytes(maximum))
				}
			}
		}
		if reason == "" {
			ready = append(ready, item)
			continue
		}
		skipped = append(skipped, fmt.Sprintf("%s（%s）：%s", safeAttachmentName(item.Name), formatBytes(actualSize), reason))
	}
	if len(skipped) > 0 {
		message.Sections = append(append([]Section(nil), message.Sections...), Section{
			Heading: "未发送的附件", Paragraphs: skipped,
		})
	}
	return message, ready
}

func safeAttachmentName(value string) string {
	value = strings.TrimSpace(filepath.Base(strings.ReplaceAll(value, `\`, "/")))
	if value == "" || value == "." {
		return "附件"
	}
	return value
}

func formatBytes(size int64) string {
	if size < 0 {
		size = 0
	}
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	units := []string{"KiB", "MiB", "GiB"}
	value := float64(size) / 1024
	for index, unit := range units {
		if value < 1024 || index == len(units)-1 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
		value /= 1024
	}
	return ""
}
