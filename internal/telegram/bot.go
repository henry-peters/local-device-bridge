package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

type Bot struct {
	token           string
	allowed         map[string]bool
	manager         *core.Manager
	client          *http.Client
	logger          Logger
	allowPublic     bool
	callbackMu      sync.RWMutex
	callbackDevices map[string]core.DeviceID
}

type Logger interface{ Printf(string, ...any) }

func New(token string, allowedIDs []string, manager *core.Manager, logger Logger) *Bot {
	allowed := map[string]bool{}
	for _, id := range allowedIDs {
		allowed[strings.TrimSpace(id)] = true
	}
	return &Bot{token: token, allowed: allowed, manager: manager, client: &http.Client{Timeout: 40 * time.Second}, logger: logger, callbackDevices: map[string]core.DeviceID{}}
}

// SetAllowPublic enables the explicitly opt-in mode where any Telegram user
// who finds the bot can send commands. Private allowlists remain the default.
func (b *Bot) SetAllowPublic(enabled bool) {
	b.allowPublic = enabled
}

func (b *Bot) Run(ctx context.Context) error {
	if b.token == "" {
		return fmt.Errorf("Telegram bot token is empty")
	}
	if !b.allowPublic && len(b.allowed) == 0 {
		return fmt.Errorf("Telegram allowlist is empty; refusing to start")
	}
	var offset int64
	for {
		updates, err := b.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if b.logger != nil {
				b.logger.Printf("telegram poll: %v", err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
				continue
			}
		}
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if update.CallbackQuery != nil {
				query := update.CallbackQuery
				identity := strconv.FormatInt(query.From.ID, 10)
				chatID := ""
				if query.Message != nil {
					chatID = strconv.FormatInt(query.Message.Chat.ID, 10)
				}
				if !b.authorized(identity, chatID) {
					_ = b.answerCallback(ctx, query.ID, "Not authorized")
					continue
				}
				response := b.handleCallback(ctx, query.Data)
				if err := b.answerCallback(ctx, query.ID, response); err != nil && b.logger != nil {
					b.logger.Printf("telegram callback reply: %v", err)
				}
				continue
			}
			if update.Message == nil {
				continue
			}
			identity := strconv.FormatInt(update.Message.From.ID, 10)
			chatID := strconv.FormatInt(update.Message.Chat.ID, 10)
			if !b.authorized(identity, chatID) {
				continue
			}
			response := b.handle(ctx, update.Message.Text)
			var sendErr error
			parts := strings.Fields(strings.TrimSpace(update.Message.Text))
			commandName := ""
			if len(parts) > 0 {
				commandName = strings.ToLower(strings.SplitN(parts[0], "@", 2)[0])
			}
			if commandName == "/remote" {
				var markup any
				response, markup = b.remoteReply(parts[1:])
				sendErr = b.sendMessageWithKeyboard(ctx, chatID, response, markup)
			} else if commandName == "/tv" && len(parts) > 1 {
				// `/tv <device>` is the friendly entry point: when the device
				// name is complete, show a tap-based command menu instead of
				// making the user type a second long command.
				if _, found := b.findDevice(strings.Join(parts[1:], " ")); found {
					var markup any
					response, markup = b.tvMenu(parts[1:])
					sendErr = b.sendMessageWithKeyboard(ctx, chatID, response, markup)
				} else {
					sendErr = b.sendMessage(ctx, chatID, response)
				}
			} else {
				sendErr = b.sendMessage(ctx, chatID, response)
			}
			if sendErr != nil && b.logger != nil {
				b.logger.Printf("telegram reply: %v", sendErr)
			}
		}
	}
}

func (b *Bot) authorized(userID, chatID string) bool {
	return b.allowPublic || b.allowed[userID] || (chatID != "" && b.allowed[chatID])
}

type update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *message       `json:"message"`
	CallbackQuery *callbackQuery `json:"callback_query"`
}
type message struct {
	Text string `json:"text"`
	From user   `json:"from"`
	Chat chat   `json:"chat"`
}
type callbackQuery struct {
	ID      string           `json:"id"`
	From    user             `json:"from"`
	Message *callbackMessage `json:"message"`
	Data    string           `json:"data"`
}
type callbackMessage struct {
	Chat chat `json:"chat"`
}
type user struct {
	ID int64 `json:"id"`
}
type chat struct {
	ID int64 `json:"id"`
}
type response[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	Description string `json:"description"`
}

func (b *Bot) getUpdates(ctx context.Context, offset int64) ([]update, error) {
	endpoint := b.endpoint("getUpdates") + "?" + url.Values{"timeout": {"30"}, "offset": {strconv.FormatInt(offset, 10)}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, b.safeNetworkError(err)
	}
	defer resp.Body.Close()
	var result response[[]update]
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, fmt.Errorf("telegram API: %s", result.Description)
	}
	return result.Result, nil
}

func (b *Bot) sendMessage(ctx context.Context, chatID, text string) error {
	return b.sendMessageWithKeyboard(ctx, chatID, text, nil)
}

func (b *Bot) sendMessageWithKeyboard(ctx context.Context, chatID, text string, keyboard any) error {
	form := url.Values{"chat_id": {chatID}, "text": {text}}
	if keyboard != nil {
		markup, err := json.Marshal(keyboard)
		if err != nil {
			return err
		}
		form.Set("reply_markup", string(markup))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint("sendMessage"), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.client.Do(req)
	if err != nil {
		return b.safeNetworkError(err)
	}
	defer resp.Body.Close()
	var result response[json.RawMessage]
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("telegram API: %s", result.Description)
	}
	return nil
}

func (b *Bot) answerCallback(ctx context.Context, queryID, text string) error {
	text = strings.TrimSpace(text)
	if len([]rune(text)) > 190 {
		text = string([]rune(text)[:190]) + "…"
	}
	form := url.Values{"callback_query_id": {queryID}, "text": {text}, "show_alert": {"false"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint("answerCallbackQuery"), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.client.Do(req)
	if err != nil {
		return b.safeNetworkError(err)
	}
	defer resp.Body.Close()
	var result response[json.RawMessage]
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("telegram API: %s", result.Description)
	}
	return nil
}

func (b *Bot) endpoint(method string) string {
	return "https://api.telegram.org/bot" + b.token + "/" + method
}

func (b *Bot) safeNetworkError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if b.token != "" {
		message = strings.ReplaceAll(message, b.token, "<redacted>")
	}
	return fmt.Errorf("telegram request failed: %s", message)
}

func (b *Bot) handle(ctx context.Context, text string) string {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) == 0 {
		return help()
	}
	command := strings.ToLower(parts[0])
	if at := strings.IndexByte(command, '@'); at >= 0 {
		command = command[:at]
	}
	switch command {
	case "/start", "/help":
		return help()
	case "/devices":
		items := b.manager.List()
		if len(items) == 0 {
			return "No devices discovered. Try /scan."
		}
		var lines []string
		for _, item := range items {
			label := core.DisplayName(item)
			if item.Alias != "" && item.Name != "" {
				label += fmt.Sprintf(" [%s]", item.Name)
			}
			lines = append(lines, fmt.Sprintf("%s — %s (%s)", label, item.ID, online(item.Online)))
		}
		return strings.Join(lines, "\n")
	case "/scan":
		found, err := b.manager.Scan(ctx)
		if err != nil {
			return "Scan failed: " + err.Error()
		}
		return fmt.Sprintf("Scan complete: %d device(s) found.", len(found))
	case "/commands":
		return commandReference()
	case "/remote":
		return "Usage: /remote <tv-name-or-id>"
	case "/tv", "/device":
		return b.tv(ctx, parts[1:])
	case "/power":
		return b.power(ctx, parts[1:])
	case "/volume":
		return b.volumeShortcut(ctx, parts[1:])
	case "/key":
		return b.keyShortcut(ctx, parts[1:])
	case "/mac":
		return b.mac(ctx, parts[1:])
	default:
		return help()
	}
}

func (b *Bot) tv(ctx context.Context, args []string) string {
	if len(args) == 0 {
		return "Choose a device first: /devices, then /tv <device name>\n\nThat opens a tap menu. For typed commands use /tv <device> help."
	}
	actionNames := map[string]bool{"help": true, "status": true, "power": true, "on": true, "off": true, "volume_up": true, "volume_down": true, "volume": true, "vol": true, "mute": true, "key": true, "source": true, "channel": true, "play": true, "pause": true, "up": true, "down": true, "left": true, "right": true, "enter": true, "back": true, "home": true}
	actionIndex := -1
	for index, value := range args {
		if actionNames[strings.ToLower(value)] {
			actionIndex = index
			break
		}
	}
	if actionIndex <= 0 {
		return "Usage: /tv <device> <status|power on|power off|volume +3|-3|20|mute|key KEY>\nTip: /tv <device> opens buttons when the device name is complete."
	}
	device, ok := b.findDevice(strings.Join(args[:actionIndex], " "))
	if !ok {
		return "Device not found or name is ambiguous. Use /devices and copy its short name or ID."
	}
	actionName := strings.ToLower(args[actionIndex])
	if actionName == "help" {
		return tvHelp(device)
	}
	if actionName == "power" {
		if len(args) <= actionIndex+1 {
			return "Usage: /tv <device> power on|off"
		}
		actionName = strings.ToLower(args[actionIndex+1])
		args = append(args[:actionIndex+1], args[actionIndex+2:]...)
	}
	keyAliases := map[string]string{"play": "PLAY", "pause": "PLAY", "up": "UP", "down": "DOWN", "left": "LEFT", "right": "RIGHT", "enter": "ENTER", "back": "RETURN", "home": "HOME"}
	if key, isKey := keyAliases[actionName]; isKey {
		actionName = "key"
		args = append(args[:actionIndex+1], key)
	}
	action := map[string]core.Action{"status": core.ActionStatus, "on": core.ActionPowerOn, "off": core.ActionPowerOff, "volume_up": core.ActionVolumeUp, "volume_down": core.ActionVolumeDown, "volume": core.ActionVolumeSet, "vol": core.ActionVolumeSet, "mute": core.ActionMute, "key": core.ActionKey, "source": core.ActionSource, "channel": core.ActionChannel}[actionName]
	if action == "" {
		return "Unsupported TV command."
	}
	arguments := map[string]string{}
	valueArgs := args[actionIndex+1:]
	relativeVolume := false
	if (actionName == "volume" || actionName == "vol") && len(valueArgs) > 0 && (strings.EqualFold(valueArgs[0], "up") || strings.EqualFold(valueArgs[0], "down")) {
		if strings.EqualFold(valueArgs[0], "up") {
			action = core.ActionVolumeUp
		} else {
			action = core.ActionVolumeDown
		}
		valueArgs = valueArgs[1:]
		if len(valueArgs) == 0 {
			valueArgs = []string{"1"}
		}
	}
	if len(valueArgs) >= 1 {
		if action == core.ActionKey {
			arguments["key"] = valueArgs[0]
		}
		if action == core.ActionVolumeSet {
			if strings.HasPrefix(valueArgs[0], "+") || strings.HasPrefix(valueArgs[0], "-") {
				if strings.HasPrefix(valueArgs[0], "+") {
					action = core.ActionVolumeUp
				} else {
					action = core.ActionVolumeDown
				}
				arguments["steps"] = strings.TrimLeft(valueArgs[0], "+-")
				relativeVolume = true
			} else {
				arguments["volume"] = valueArgs[0]
			}
		}
		if action == core.ActionSource {
			arguments["source"] = valueArgs[0]
		}
		if action == core.ActionChannel {
			arguments["channel"] = valueArgs[0]
		}
	}
	if action == core.ActionVolumeUp || action == core.ActionVolumeDown {
		if len(valueArgs) >= 1 && !relativeVolume {
			parsed, parseErr := strconv.Atoi(valueArgs[0])
			if parseErr != nil || parsed < 1 || parsed > 20 {
				return "Volume steps must be a number from 1 to 20."
			}
			arguments["steps"] = valueArgs[0]
		}
	}
	if (action == core.ActionKey || action == core.ActionVolumeSet || action == core.ActionSource || action == core.ActionChannel) && len(valueArgs) == 0 {
		return "That command needs one value. Use /commands for examples."
	}
	return b.executeTV(ctx, device, action, arguments)
}

func (b *Bot) executeTV(ctx context.Context, device core.DeviceMetadata, action core.Action, arguments map[string]string) string {
	result, err := b.manager.Execute(ctx, core.Command{DeviceID: device.ID, Action: action, Arguments: arguments, Principal: "telegram", Source: "telegram"})
	if err != nil {
		return "Command failed: " + err.Error()
	}
	return result.Message
}

func (b *Bot) power(ctx context.Context, args []string) string {
	if len(args) < 2 || (strings.ToLower(args[len(args)-1]) != "on" && strings.ToLower(args[len(args)-1]) != "off") {
		return "Usage: /power <name-or-id> on|off"
	}
	device, ok := b.findDevice(strings.Join(args[:len(args)-1], " "))
	if !ok {
		return "Device not found or name is ambiguous. Use /devices first."
	}
	action := core.ActionPowerOff
	if strings.EqualFold(args[len(args)-1], "on") {
		action = core.ActionPowerOn
	}
	result, err := b.manager.Execute(ctx, core.Command{DeviceID: device.ID, Action: action, Principal: "telegram", Source: "telegram"})
	if err != nil {
		return "Command failed: " + err.Error()
	}
	return result.Message
}

func (b *Bot) volumeShortcut(ctx context.Context, args []string) string {
	if len(args) < 2 {
		return "Usage: /volume <name-or-id> +3|-3|20"
	}
	device, ok := b.findDevice(strings.Join(args[:len(args)-1], " "))
	if !ok {
		return "Device not found or name is ambiguous. Use /devices first."
	}
	value := strings.TrimSpace(args[len(args)-1])
	action := core.ActionVolumeSet
	arguments := map[string]string{}
	if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		action = core.ActionVolumeUp
		if strings.HasPrefix(value, "-") {
			action = core.ActionVolumeDown
		}
		arguments["steps"] = strings.TrimPrefix(strings.TrimPrefix(value, "+"), "-")
	} else {
		arguments["volume"] = value
	}
	result, err := b.manager.Execute(ctx, core.Command{DeviceID: device.ID, Action: action, Arguments: arguments, Principal: "telegram", Source: "telegram"})
	if err != nil {
		return "Command failed: " + err.Error()
	}
	return result.Message
}

func (b *Bot) keyShortcut(ctx context.Context, args []string) string {
	if len(args) < 2 {
		return "Usage: /key <name-or-id> HOME|UP|DOWN|LEFT|RIGHT|ENTER|BACK|PLAY"
	}
	device, ok := b.findDevice(strings.Join(args[:len(args)-1], " "))
	if !ok {
		return "Device not found or name is ambiguous. Use /devices first."
	}
	result, err := b.manager.Execute(ctx, core.Command{DeviceID: device.ID, Action: core.ActionKey, Arguments: map[string]string{"key": strings.ToUpper(args[len(args)-1])}, Principal: "telegram", Source: "telegram"})
	if err != nil {
		return "Command failed: " + err.Error()
	}
	return result.Message
}

func (b *Bot) findDevice(reference string) (core.DeviceMetadata, bool) {
	reference = strings.TrimSpace(reference)
	items := b.manager.List()
	for _, item := range items {
		if string(item.ID) == reference || strings.EqualFold(string(item.ID), reference) {
			return item, true
		}
	}
	normalized := strings.ToLower(reference)
	var match core.DeviceMetadata
	found := false
	for _, item := range items {
		if strings.ToLower(item.Alias) != normalized && strings.ToLower(item.Name) != normalized {
			continue
		}
		if found {
			return core.DeviceMetadata{}, false
		}
		match = item
		found = true
	}
	return match, found
}

func (b *Bot) remoteReply(args []string) (string, any) {
	if len(args) == 0 {
		return "Usage: /remote <tv-name-or-id>", nil
	}
	item, found := b.findDevice(strings.Join(args, " "))
	if !found {
		return "Device not found or name is ambiguous. Use /devices first.", nil
	}
	id := item.ID
	var name string
	for _, item := range b.manager.List() {
		if item.ID == id {
			name = core.DisplayName(item)
			if item.Kind != core.DeviceKindTV || containsUnsupported(item.Capabilities) {
				return "This device does not have a local TV remote adapter yet.", nil
			}
			break
		}
	}
	alias := b.callbackAlias(id)
	button := func(label, action string) map[string]string {
		return map[string]string{"text": label, "callback_data": "r:" + alias + ":" + action}
	}
	keyboard := map[string]any{"inline_keyboard": [][]map[string]string{
		{button("Wake TV", "on"), button("Turn off", "off")},
		{button("Vol −", "vd"), button("Mute", "mute"), button("Vol +", "vu")},
		{button("↑", "key:UP")},
		{button("←", "key:LEFT"), button("OK", "key:ENTER"), button("→", "key:RIGHT")},
		{button("↓", "key:DOWN")},
		{button("Home", "key:HOME"), button("Back", "key:RETURN"), button("Play / Pause", "key:PLAY")},
	}}
	return fmt.Sprintf("Remote control: %s\nTap a button to send one command. Use Vol + or Vol − repeatedly for volume.", name), keyboard
}

func tvHelp(device core.DeviceMetadata) string {
	name := core.DisplayName(device)
	return fmt.Sprintf(`TV controls for %s

Tap menu: /tv %s
Status:   /tv %s status
Power:    /tv %s power on   or   /tv %s power off
Volume:   /tv %s volume up [steps]
          /tv %s volume down [steps]
          /tv %s volume 20
Mute:     /tv %s mute
Remote:   /tv %s key UP|DOWN|LEFT|RIGHT|ENTER|RETURN|HOME|PLAY

	Use /devices to see the exact device names and /scan to refresh the inventory.`, name, name, name, name, name, name, name, name, name, name)
}

func (b *Bot) tvMenu(args []string) (string, any) {
	device, found := b.findDevice(strings.Join(args, " "))
	if !found {
		return "Device not found or name is ambiguous. Use /devices first.", nil
	}
	alias := b.callbackAlias(device.ID)
	button := func(label, action string) map[string]string {
		return map[string]string{"text": label, "callback_data": "t:" + alias + ":" + action}
	}
	keyboard := map[string]any{"inline_keyboard": [][]map[string]string{
		{button("Status", "status"), button("Wake", "on"), button("Off", "off")},
		{button("Vol −", "vd"), button("Mute", "mute"), button("Vol +", "vu")},
		{button("↑", "key:UP")},
		{button("←", "key:LEFT"), button("OK", "key:ENTER"), button("→", "key:RIGHT")},
		{button("↓", "key:DOWN")},
		{button("Home", "key:HOME"), button("Back", "key:RETURN"), button("Play / Pause", "key:PLAY")},
	}}
	return fmt.Sprintf("TV controls: %s\nTap an action below. For the full typed list send /tv %s help.", core.DisplayName(device), core.DisplayName(device)), keyboard
}

func (b *Bot) callbackAlias(id core.DeviceID) string {
	digest := sha256.Sum256([]byte(id))
	alias := hex.EncodeToString(digest[:4])
	b.callbackMu.Lock()
	b.callbackDevices[alias] = id
	b.callbackMu.Unlock()
	return alias
}

func (b *Bot) handleCallback(ctx context.Context, data string) string {
	parts := strings.SplitN(data, ":", 3)
	if len(parts) != 3 || (parts[0] != "r" && parts[0] != "t") {
		return "Remote button expired. Send /remote again."
	}
	b.callbackMu.RLock()
	id, ok := b.callbackDevices[parts[1]]
	b.callbackMu.RUnlock()
	if !ok {
		return "Remote button expired. Send /remote again."
	}
	action := core.ActionKey
	arguments := map[string]string{}
	switch parts[2] {
	case "status":
		action = core.ActionStatus
	case "on":
		action = core.ActionPowerOn
	case "off":
		action = core.ActionPowerOff
	case "vu":
		action = core.ActionVolumeUp
	case "vd":
		action = core.ActionVolumeDown
	case "mute":
		action = core.ActionMute
	default:
		if strings.HasPrefix(parts[2], "key:") {
			arguments["key"] = strings.TrimPrefix(parts[2], "key:")
		} else {
			return "Unsupported remote button."
		}
	}
	result, err := b.manager.Execute(ctx, core.Command{DeviceID: id, Action: action, Arguments: arguments, Principal: "telegram", Source: "telegram"})
	if err != nil {
		return "Command failed: " + err.Error()
	}
	return result.Message
}

func (b *Bot) mac(ctx context.Context, args []string) string {
	if len(args) != 2 {
		return "Usage: /mac <device-id> <status|wake|sleep>"
	}
	action := map[string]core.Action{"status": core.ActionStatus, "wake": core.ActionPowerOn, "sleep": core.ActionPowerOff}[strings.ToLower(args[1])]
	if action == "" {
		return "Unsupported Mac command. Use /commands."
	}
	result, err := b.manager.Execute(ctx, core.Command{DeviceID: core.DeviceID(args[0]), Action: action, Principal: "telegram", Source: "telegram"})
	if err != nil {
		return "Command failed: " + err.Error()
	}
	return result.Message
}

func online(value bool) string {
	if value {
		return "online"
	}
	return "offline"
}
func help() string {
	return "local-device-bridge\n\nStart here:\n  /devices              show the device list\n  /tv <device name>     open tap controls\n  /scan                 refresh discovery\n  /help                 show this short guide\n\nUse /commands for the organized command reference."
}

func commandReference() string {
	return `local-device-bridge Telegram commands

START HERE
/devices                         List names, IDs, platform, and online state.
/tv <device name>                Open tap controls for one device.
/tv <device name> help           Show only that device's typed commands.
/scan                            Refresh the local inventory.

TV / DISPLAY COMMANDS
/tv <device> status               Read the current state.
/tv <device> power on             Wake or power on when supported.
/tv <device> power off            Turn off when supported.
/tv <device> volume up [steps]    Increase volume; steps default to 1.
/tv <device> volume down [steps]  Decrease volume; steps default to 1.
/tv <device> volume 20            Request an absolute volume level.
/tv <device> mute                 Toggle mute.
/tv <device> key HOME             Send a remote key (UP, DOWN, LEFT, RIGHT,
                                  ENTER, RETURN, HOME, PLAY, and more).

PAIRING AND INTEGRATIONS
/remote <device>                 Open the full tap-based remote.
/commands                         Show this organized reference.
/mac <id> status|wake|sleep       Control a paired remote Mac; the bridge host
                                  is intentionally status-only.

Only configured Telegram user IDs and chat IDs can run commands.`
}

func containsUnsupported(values []core.Capability) bool {
	return len(values) == 1 && values[0] == core.CapabilityUnsupported
}
