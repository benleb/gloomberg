package notify

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/benleb/gloomberg/internal/utils"
	"github.com/charmbracelet/log"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/spf13/viper"
)

// func bulkSendTelegramMessage(chatIDs []int64, text string, imageURI string) {
// 	for _, chatID := range chatIDs {
// 		sendTelegramMessage(chatID, text, imageURI)
// 	}
// }

// func sendTelegramMessage(chatID int64, text string, imageURI string) (tgbotapi.Message, error) {
// 	return sendTelegramMessageWithMarkup(chatID, text, imageURI, 0, nil)
// }

func sendTelegramMessageWithMarkup(chatID int64, text string, imageURI string, replyToMessageID int, replyMarkup interface{}) (tgbotapi.Message, error) {
	if tgBot == nil {
		tgBot, err := GetBot()

		if err != nil || tgBot == nil {
			return tgbotapi.Message{}, err
		}
	}

	// if no photo is provided, send to the global channel
	if chatID == 0 {
		chatID = viper.GetInt64("notifications.telegram.chat_id")
	}

	log.Infof("🔔 new notification | to: %d", chatID)

	// message
	parseMode := "markdown" // "markdownv2"?
	// parseMode := "MarkdownV2"
	// disableNotifications := false

	var detectedCcontentType, headerContentType string

	var imageReader io.Reader

	// if an imageURI is provided, we try to attach it to the message
	if imageURI != "" {
		log.Infof("📸  fetching image: %s", imageURI)

		// check if imageURI points to a valid image/media file
		response, err := utils.HTTP.GetWithTLS12(context.Background(), imageURI)

		switch {
		case err == nil && response.StatusCode == http.StatusOK:
			// read image
			image, err := io.ReadAll(response.Body)
			if err != nil {
				log.Errorf("📸 ❌ error while reading imageURI: %v | http: %d | %s", err, response.StatusCode, imageURI)
			}

			// get the image as io.Reader for the telegram api
			imageReader = bytes.NewReader(image)

			// check what kind of image/content it is (by reading the first 512 bytes)
			detectedCcontentType = http.DetectContentType(image)
			// get the content type from the header
			headerContentType = response.Header.Get("Content-Type")

			log.Infof("📸 got image | http: %d | contentType hdr: %v | contentType image: %s", response.StatusCode, headerContentType, detectedCcontentType)

			if !strings.HasPrefix(detectedCcontentType, "image/") && !strings.HasPrefix(detectedCcontentType, "video/") && detectedCcontentType != "application/octet-stream" {
				log.Warnf("📸 ❔ image seems to be not an image | hdr: %s <> bytes: %s | http: %d | %s", headerContentType, detectedCcontentType, response.StatusCode, imageURI)
			}

		case err == nil && response.StatusCode != http.StatusOK:
			log.Errorf("📸 ⁉️ imageURI invalid (non-200 http status code): %v | http: %d | %s", err, response.StatusCode, imageURI)

		case err == nil:
			defer response.Body.Close()

			fallthrough

		case err != nil:
			log.Errorf("📸 ❌ error while getting image: %v || %s", err, imageURI)
		}
	}

	// based on the content type, we have to send a different type of message to telegram
	switch {
	case strings.HasPrefix(detectedCcontentType, "image/"), strings.HasSuffix(imageURI, ".jpg"), strings.HasSuffix(imageURI, ".png"):
		msg := tgbotapi.NewPhoto(chatID, tgbotapi.FileURL(imageURI))
		msg.ParseMode = parseMode
		msg.DisableNotification = false
		msg.Caption = text

		if replyToMessageID != 0 {
			msg.ReplyToMessageID = replyToMessageID
		}

		log.Info("🔔 📸 sending photo") // | msg: %+v", msg)

		return tgBot.Send(msg)

	case detectedCcontentType == "image/gif":
		extension := strings.TrimPrefix(detectedCcontentType, "image/")

		msg := tgbotapi.NewAnimation(chatID, tgbotapi.FileReader{
			Name:   "gbAnimation." + extension,
			Reader: imageReader,
		})
		msg.ParseMode = parseMode
		msg.DisableNotification = false

		msg.Caption = text

		if replyToMessageID != 0 {
			msg.ReplyToMessageID = replyToMessageID
		}

		log.Infof("🔔 📸 sending animation | msg: %+v", msg)

		return tgBot.Send(msg)

	case strings.HasPrefix(detectedCcontentType, "video/"):
		extension := strings.TrimPrefix(detectedCcontentType, "video/")

		msg := tgbotapi.NewVideo(chatID, tgbotapi.FileReader{
			Name:   "gbVideo." + extension,
			Reader: imageReader,
		})
		msg.ParseMode = parseMode
		msg.DisableNotification = false

		msg.Caption = text

		if replyToMessageID != 0 {
			msg.ReplyToMessageID = replyToMessageID
		}

		log.Infof("🔔 📸 sending video | msg: %+v", msg)

		return tgBot.Send(msg)
	}

	// if none of the above or it failed, we send a simple message without image
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = parseMode
	msg.DisableNotification = false
	// send msg to topic (topics are simply replies to a message)

	msg.ReplyMarkup = replyMarkup

	if replyToMessageID != 0 {
		msg.ReplyToMessageID = replyToMessageID
	}

	msg.DisableWebPagePreview = true

	log.Debugf("🔔 📸 sending message | msg: %+v", msg)

	return tgBot.Send(msg)
}
