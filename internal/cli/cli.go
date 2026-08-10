package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/local-device-bridge/local-device-bridge/internal/api"
	"github.com/local-device-bridge/local-device-bridge/internal/config"
	"github.com/local-device-bridge/local-device-bridge/internal/core"
	"github.com/local-device-bridge/local-device-bridge/internal/security"
	bridgeService "github.com/local-device-bridge/local-device-bridge/internal/service"
	"golang.org/x/term"
)

func ConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "config.json"
	}
	return filepath.Join(dir, "local-device-bridge", "config.json")
}

func Run(args []string) error {
	configPath := ConfigPath()
	if len(args) >= 1 && args[0] == "--config" {
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			return errors.New("usage: local-device-bridge [--config <path>] <command>")
		}
		configPath = args[1]
		args = args[2:]
	}
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help") {
		usage()
		return nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	secrets := security.NewSecretStore("local-device-bridge", cfg.State.Directory)
	if len(args) == 0 {
		return runCLIHome(configPath, cfg, secrets)
	}
	switch args[0] {
	case "cli":
		return runCLIHome(configPath, cfg, secrets)
	case "setup":
		return runSetup(configPath, cfg, secrets)
	case "dashboard":
		if len(args) > 1 && args[1] == "open" {
			return ensureDaemonAndOpen(configPath, cfg)
		}
		if len(args) > 1 && args[1] == "phone" {
			return printPhoneDashboardAccess(configPath, cfg, secrets)
		}
		if len(args) > 1 && args[1] == "token" {
			return printDashboardTokenInstructions(secrets)
		}
		if len(args) > 1 && args[1] == "cert" {
			fmt.Println(dashboardCertificatePath(cfg))
			return nil
		}
		if len(args) > 1 && args[1] == "trust" {
			printDashboardTrust(cfg)
			return nil
		}
		token, err := api.EnsureAccessToken(secrets)
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil
	case "service":
		return runService(configPath, args[1:])
	case "discover":
		return call(cfg, secrets, http.MethodPost, "/api/v1/discovery/scan", nil, true)
	case "devices":
		if len(args) > 1 && args[1] == "list" {
			return call(cfg, secrets, http.MethodGet, "/api/v1/devices", nil, true)
		}
		if len(args) > 3 && args[1] == "rename" {
			return call(cfg, secrets, http.MethodPost, deviceAPIPath(args[2], "/name"), map[string]any{"name": strings.Join(args[3:], " ")}, true)
		}
		return errors.New("usage: local-device-bridge devices list | devices rename <id-or-name> <friendly name>")
	case "pair":
		if len(args) != 2 {
			return errors.New("usage: local-device-bridge pair <id-or-name> (Mac account name is requested privately when needed)")
		}
		return pairDevice(cfg, secrets, args[1])
	case "unpair":
		if len(args) != 2 {
			return errors.New("usage: local-device-bridge unpair <device-id>")
		}
		return call(cfg, secrets, http.MethodPost, deviceAPIPath(args[1], "/unpair"), nil, true)
	case "commands":
		printCommands()
		return nil
	case "agent":
		return runAgent(cfg, secrets, args[1:])
	case "remote":
		return runRemote(cfg, secrets, args[1:])
	case "mac":
		return runMac(cfg, secrets, args[1:])
	case "tv", "device":
		return runTV(cfg, secrets, args[1:])
	default:
		return fmt.Errorf("unknown command %q; run local-device-bridge help", args[0])
	}
}

func runAgent(cfg config.Config, secrets *security.SecretStore, args []string) error {
	if len(args) == 1 && args[0] == "token" {
		token, err := api.EnsureAgentToken(secrets)
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil
	}
	if len(args) == 1 && args[0] == "manifest" {
		return call(cfg, secrets, http.MethodGet, "/api/v1/agent/manifest", nil, false)
	}
	if len(args) == 1 && args[0] == "openapi" {
		return call(cfg, secrets, http.MethodGet, "/api/v1/agent/openapi.json", nil, false)
	}
	if len(args) == 2 && args[0] == "guide" {
		return call(cfg, secrets, http.MethodGet, "/api/v1/devices/"+url.PathEscape(args[1])+"/guide", nil, false)
	}
	return errors.New("usage: local-device-bridge agent manifest | agent openapi | agent guide <device-id> | agent token")
}

func runCLIHome(configPath string, cfg config.Config, secrets *security.SecretStore) error {
	printBanner()
	theme := currentTheme()
	fmt.Println(theme.bold("CLI HOME  //  CONTROL NODE"))
	fmt.Println(theme.dim("The terminal is the main control surface. The dashboard is optional."))
	if cfg.CLI.DashboardEnabled {
		if cfg.CLI.AutoLaunchDashboard {
			fmt.Println(theme.green("Experience: CLI + dashboard · dashboard opens after setup"))
		} else {
			fmt.Println(theme.cyan("Experience: CLI + dashboard · open the dashboard when you choose"))
		}
	} else {
		fmt.Println(theme.cyan("Experience: CLI only · dashboard launch is disabled"))
	}
	fmt.Println(theme.dim("Inventory: TVs/displays, consoles, macOS, Windows, and Raspberry Pi"))
	printDashboardLinks(cfg, theme)

	fmt.Println()
	fmt.Println(theme.cyan(theme.bold("NETWORK INVENTORY")))
	if dashboardHealth(cfg) {
		output, err := request(cfg, secrets, http.MethodGet, "/api/v1/devices", nil)
		if err != nil {
			fmt.Println(theme.yellow("The daemon is running, but the inventory could not be loaded."))
			fmt.Println(theme.dim(err.Error()))
		} else {
			renderInventory(output)
		}
	} else {
		fmt.Println(theme.yellow("Daemon is not running yet; the inventory will appear after it starts."))
		fmt.Println(theme.dim("Start it with: local-device-bridge daemon"))
	}
	printRecommendedSteps(cfg, theme)

	fmt.Println()
	fmt.Println(theme.cyan(theme.bold("QUICK COMMANDS")))
	fmt.Println("  discover                         scan the local network")
	fmt.Println("  devices list                     show the current inventory")
	fmt.Println("  pair <device-id>                 pair a supported device")
	fmt.Println("  remote <supported-tv-id>         open the keyboard remote")
	fmt.Println("  commands                         show every command and option")
	fmt.Println("  setup                            change dashboard, inventory, or chat settings")

	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return nil
	}
	for {
		fmt.Println()
		choice, err := selectMenu(bufio.NewReader(os.Stdin), "What would you like to do?", []menuOption{
			{Label: "Refresh device list", Description: "read the daemon's current inventory"},
			{Label: "Scan network", Description: "look for new devices and address changes"},
			{Label: "Open dashboard", Description: "launch the browser control center"},
			{Label: "Phone dashboard access", Description: "show the one-time phone link, QR code, and Agent API shortcut"},
			{Label: "Command reference", Description: "show all CLI commands"},
			{Label: "Run setup again", Description: "change saved preferences"},
			{Label: "Quit", Description: "return to the shell"},
		}, 0)
		if err != nil {
			return err
		}
		switch choice {
		case 0:
			output, err := requestWithProgress(cfg, secrets, http.MethodGet, "/api/v1/devices", nil)
			if err != nil {
				fmt.Println(theme.red("Inventory failed: " + err.Error()))
			} else {
				renderInventory(output)
			}
		case 1:
			output, err := requestWithProgress(cfg, secrets, http.MethodPost, "/api/v1/discovery/scan", nil)
			if err != nil {
				fmt.Println(theme.red("Network scan failed: " + err.Error()))
			} else {
				renderInventory(output)
			}
		case 2:
			if !cfg.CLI.DashboardEnabled {
				fmt.Println(theme.yellow("The dashboard is disabled. Run setup and choose CLI + dashboard first."))
				continue
			}
			if err := ensureDaemonAndOpen(configPath, cfg); err != nil {
				fmt.Println(theme.red("Dashboard could not be opened: " + err.Error()))
			}
		case 3:
			if !cfg.Server.AllowLAN {
				fmt.Println(theme.yellow("Phone access is disabled. Run setup and choose 'This computer + my phone' first."))
				continue
			}
			if err := printPhoneDashboardAccess(configPath, cfg, secrets); err != nil {
				fmt.Println(theme.red("Phone dashboard could not be prepared: " + err.Error()))
			}
		case 4:
			printCommands()
		case 5:
			return runSetup(configPath, cfg, secrets)
		case 6:
			return nil
		}
	}
}

func printRecommendedSteps(cfg config.Config, theme cliTheme) {
	fmt.Println()
	fmt.Println(theme.cyan(theme.bold("RECOMMENDED NEXT STEPS")))
	fmt.Println(theme.dim("Use this order when setting up a new bridge:"))

	fmt.Printf("  %s  %s\n", theme.green("01"), theme.bold("Discover devices"))
	fmt.Println("      local-device-bridge discover")
	fmt.Printf("  %s  %s\n", theme.green("02"), theme.bold("List the inventory"))
	fmt.Println("      local-device-bridge devices list")
	fmt.Printf("  %s  %s\n", theme.green("03"), theme.bold("Pair a supported device"))
	fmt.Println("      local-device-bridge pair <id-or-name>")
	fmt.Printf("  %s  %s\n", theme.green("04"), theme.bold("Control it"))
	fmt.Println("      local-device-bridge device <id-or-name> help")

	if cfg.CLI.DashboardEnabled {
		fmt.Println()
		fmt.Println(theme.blue(theme.bold("PHONE RECOMMENDATION")))
		if cfg.Server.AllowLAN {
			fmt.Println("  Run local-device-bridge dashboard phone to print a clickable link and QR code.")
			fmt.Println("  Scan it with the phone Camera app, sign in once, then tap Agent API for the command guide.")
		} else {
			fmt.Println("  Phone access is off. Run local-device-bridge setup and choose 'This computer + my phone'.")
		}
	}

	if cfg.Telegram.Enabled {
		fmt.Println()
		fmt.Println(theme.yellow(theme.bold("CHAT RECOMMENDATION")))
		fmt.Println("  Telegram is enabled with the configured access policy.")
		fmt.Println("  Use /help, /devices, /scan, and /tv <name> to keep commands short and readable.")
	}
}

func pairDevice(cfg config.Config, secrets *security.SecretStore, reference string) error {
	output, err := request(cfg, secrets, http.MethodGet, "/api/v1/devices", nil)
	if err != nil {
		return err
	}
	device, err := findDevice(output, reference)
	if err != nil {
		return err
	}
	body := map[string]string(nil)
	if isRemoteMacDevice(device) {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("Mac pairing needs the target Mac's short account name; run this command from an interactive terminal or use the dashboard. It is intentionally not accepted as a command-line argument")
		}
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("Target Mac short account name (not the device name or email): ")
		username, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		username = strings.TrimSpace(username)
		if username == "" || strings.HasPrefix(username, "-") {
			return errors.New("a valid short Mac account name is required; it is never passed as a shell option")
		}
		body = map[string]string{"username": username}
	}
	return call(cfg, secrets, http.MethodPost, deviceAPIPath(fmt.Sprint(device["id"]), "/pair"), body, true)
}

func isRemoteMacDevice(device map[string]any) bool {
	return strings.EqualFold(fmt.Sprint(device["kind"]), string(core.DeviceKindComputer)) &&
		strings.EqualFold(fmt.Sprint(device["manufacturer"]), "Apple") &&
		!strings.EqualFold(fmt.Sprint(device["discovery"]), "host")
}

func runSetup(configPath string, cfg config.Config, secrets *security.SecretStore) error {
	printBanner()
	reader := bufio.NewReader(os.Stdin)
	fmt.Println()
	theme := currentTheme()
	fmt.Println(theme.bold("Welcome. Let’s configure your bridge."))
	fmt.Println(theme.dim("Choose one step at a time with ↑/↓ and Enter."))
	fmt.Println(theme.dim("Recommended: CLI + local dashboard, known devices plus discovery, and private Telegram access."))
	fmt.Println(theme.dim("TV credentials and the Telegram token stay in the OS keychain."))

	printStep(1, "START EXPERIENCE", "Choose whether the browser or terminal is your default starting point.")
	dashboardMode, err := selectMenu(reader, "How should local-device-bridge start after setup?", []menuOption{
		{Label: "CLI + dashboard", Description: "use the terminal and open the browser control center"},
		{Label: "CLI only", Description: "stay in the terminal; no automatic browser launch"},
	}, map[bool]int{true: 0, false: 1}[cfg.CLI.DashboardEnabled])
	if err != nil {
		return err
	}
	cfg.CLI.DashboardEnabled = dashboardMode == 0
	if !cfg.CLI.DashboardEnabled {
		cfg.CLI.AutoLaunchDashboard = false
		cfg.CLI.ShowDashboardURL = false
	} else {
		autoLaunch, autoErr := selectMenu(reader, "Open the dashboard automatically when setup finishes?", []menuOption{
			{Label: "Yes, open it for me", Description: "start the daemon if needed and launch the browser"},
			{Label: "No, I will open it myself", Description: "keep the terminal in control"},
		}, map[bool]int{true: 0, false: 1}[cfg.CLI.AutoLaunchDashboard])
		if autoErr != nil {
			return autoErr
		}
		cfg.CLI.AutoLaunchDashboard = autoLaunch == 0
		showURL, urlErr := selectMenu(reader, "Show the dashboard link after setup?", []menuOption{
			{Label: "Show the link", Description: "print the local or phone URL for quick access"},
			{Label: "Keep it hidden", Description: "open automatically or use the dashboard command later"},
		}, map[bool]int{true: 0, false: 1}[cfg.CLI.ShowDashboardURL])
		if urlErr != nil {
			return urlErr
		}
		cfg.CLI.ShowDashboardURL = showURL == 0
	}

	printStep(2, "DASHBOARD ACCESS", "Choose where the browser control center can be opened.")
	if cfg.CLI.DashboardEnabled {
		lan, lanErr := selectMenu(reader, "Where do you want to use the dashboard?", []menuOption{
			{Label: "This computer only", Description: "localhost only; no phone link"},
			{Label: "This computer + my phone", Description: "phone link on the same Wi-Fi"},
		}, map[bool]int{false: 0, true: 1}[cfg.Server.AllowLAN])
		if lanErr != nil {
			return lanErr
		}
		cfg.Server.AllowLAN = lan == 1
	} else {
		fmt.Println(theme.dim("CLI-only mode keeps the dashboard service private on this computer."))
		cfg.Server.AllowLAN = false
	}
	if cfg.Server.AllowLAN {
		cfg.Server.Bind = "0.0.0.0:8787"
		httpChoice, httpErr := selectMenu(reader, "Which phone connection should be available?", []menuOption{
			{Label: "HTTP on trusted home Wi-Fi (recommended)", Description: "no certificate warning; token-protected; never expose it publicly"},
			{Label: "HTTPS with local certificate", Description: "encrypted; each browser may need to trust the generated certificate"},
		}, map[bool]int{true: 0, false: 1}[cfg.Server.InsecureLANHTTP])
		if httpErr != nil {
			return httpErr
		}
		cfg.Server.InsecureLANHTTP = httpChoice == 0
	} else {
		cfg.Server.Bind = "127.0.0.1:8787"
		cfg.Server.InsecureLANHTTP = false
	}

	printStep(3, "DEVICE INVENTORY", "Choose the product groups shown in the dashboard, CLI, and Telegram.")
	inventory, err := selectMultiMenu(reader, "Visible product groups", []menuOption{
		{Label: "TVs & displays", Description: "Samsung, Roku, LG, Sony, and identified screens"},
		{Label: "Game consoles", Description: "PlayStation, Xbox, and Nintendo discovery plus safe Wake-on-LAN"},
		{Label: "Computers", Description: "macOS, identified Windows computers, and Raspberry Pi"},
	}, []bool{cfg.Discovery.ShowDisplayDevices, cfg.Discovery.ShowConsoleDevices, cfg.Discovery.ShowComputerDevices})
	if err != nil {
		return err
	}
	cfg.Discovery.ShowDisplayDevices = inventory[0]
	cfg.Discovery.ShowConsoleDevices = inventory[1]
	cfg.Discovery.ShowComputerDevices = inventory[2]

	printStep(4, "CHAT CONNECTIONS", "First choose whether to configure chat integrations. Telegram is optional.")
	configureChats, err := selectMenu(reader, "Configure chat options now?", []menuOption{
		{Label: "Yes — configure chat options", Description: "choose Telegram and its access rules"},
		{Label: "No — skip chat setup", Description: "leave chat integrations off; you can run setup again later"},
	}, map[bool]int{true: 0, false: 1}[cfg.Telegram.Enabled])
	if err != nil {
		return err
	}
	cfg.Telegram.Enabled = false
	telegramToken := ""
	if configureChats == 0 {
		chatSelections, selectErr := selectMultiMenu(reader, "Which chat services should be enabled?", []menuOption{
			{Label: "Telegram", Description: "buttons, commands, and multiple allowed chats"},
		}, []bool{cfg.Telegram.Enabled})
		if selectErr != nil {
			return selectErr
		}
		cfg.Telegram.Enabled = chatSelections[0]
	}
	if cfg.Telegram.Enabled {
		printStep(5, "TELEGRAM BOT", "Add the bot token, allowed chats, and access level.")
		fmt.Println()
		fmt.Println("Create a bot with @BotFather. The token is stored in the OS keychain, not config.json.")
		fmt.Printf("Bot token (hidden; Enter keeps the saved token or uses %s): ", cfg.Telegram.TokenEnv)
		telegramToken, err = readSecretInput(reader)
		if err != nil {
			return err
		}
		fmt.Println("Add one or more allowed Telegram user/chat IDs.")
		fmt.Print("Allowed IDs (comma-separated, optional in public mode): ")
		ids, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		cfg.Telegram.AllowedIDs = splitIDs(ids)
		publicChoice, publicErr := selectMenu(reader, "Who can send Telegram commands?", []menuOption{
			{Label: "Private allowlist", Description: "only the IDs entered above; recommended"},
			{Label: "Public commands", Description: "any Telegram account that finds the bot; risky"},
		}, map[bool]int{false: 0, true: 1}[cfg.Telegram.AllowPublic])
		if publicErr != nil {
			return publicErr
		}
		cfg.Telegram.AllowPublic = publicChoice == 1
		if cfg.Telegram.AllowPublic {
			fmt.Println(theme.red("Warning: public commands let any Telegram user control this bridge."))
			fmt.Print("Type PUBLIC to confirm, or press Enter to keep the private allowlist: ")
			confirmation, confirmErr := reader.ReadString('\n')
			if confirmErr != nil && !errors.Is(confirmErr, io.EOF) {
				return confirmErr
			}
			if strings.TrimSpace(confirmation) != "PUBLIC" {
				cfg.Telegram.AllowPublic = false
				fmt.Println("Public commands not enabled; private allowlist selected.")
			}
		}
	}

	saveStep := 5
	if cfg.Telegram.Enabled {
		saveStep = 6
	}
	printStep(saveStep, "SAVE AND START", "Review the selected setup before writing the configuration.")
	fmt.Printf("Experience: %s\n", experienceLabel(cfg))
	if cfg.CLI.DashboardEnabled {
		fmt.Printf("Dashboard launch: %s\n", dashboardLaunchLabel(cfg.CLI.AutoLaunchDashboard))
		fmt.Printf("Dashboard link: %s\n", dashboardURLLabel(cfg.CLI.ShowDashboardURL))
		if cfg.Server.AllowLAN {
			fmt.Println("Phone dashboard: enabled on the same Wi-Fi")
			if cfg.Server.InsecureLANHTTP {
				fmt.Println("Phone HTTP compatibility: enabled on the main LAN port (token-protected; trusted LAN only)")
			}
		} else {
			fmt.Println("Phone dashboard: disabled")
		}
	}
	fmt.Printf("Product groups: TVs/displays %s · macOS/Windows/Raspberry Pi computers %s\n", enabledLabel(cfg.Discovery.ShowDisplayDevices), enabledLabel(cfg.Discovery.ShowComputerDevices))
	fmt.Printf("Telegram: %s\n", enabledLabel(cfg.Telegram.Enabled))
	if cfg.Telegram.Enabled {
		fmt.Printf("Telegram access: %s\n", telegramAccessLabel(cfg.Telegram.AllowPublic))
	}
	choice, err := selectMenu(reader, "Ready to save?", []menuOption{
		{Label: "Save configuration", Description: "write these choices and continue"},
		{Label: "Cancel", Description: "leave the existing configuration unchanged"},
	}, 0)
	if err != nil {
		return err
	}
	if choice == 1 {
		fmt.Println("Setup cancelled. No configuration was changed.")
		return nil
	}
	showPhoneAccess := false
	if cfg.CLI.DashboardEnabled && cfg.Server.AllowLAN {
		phoneChoice, phoneErr := selectMenu(reader, "Show phone dashboard access now?", []menuOption{
			{Label: "Yes — start the phone service and show link + QR", Description: "the daemon starts once, then the one-time phone link and QR are printed"},
			{Label: "No — finish in the terminal", Description: "run local-device-bridge dashboard phone whenever you want the link later"},
		}, 0)
		if phoneErr != nil {
			return phoneErr
		}
		showPhoneAccess = phoneChoice == 0
	}
	return saveSetup(configPath, cfg, secrets, telegramToken, showPhoneAccess)
}

func saveSetup(configPath string, cfg config.Config, secrets *security.SecretStore, telegramToken string, showPhoneAccess bool) error {
	theme := currentTheme()
	if telegramToken != "" {
		if err := secrets.Set("telegram_bot_token", telegramToken); err != nil {
			return fmt.Errorf("save Telegram token securely: %w", err)
		}
	}
	if err := config.Save(configPath, cfg); err != nil {
		return err
	}
	serviceConfigured := false
	serviceWarning := ""
	if cfg.CLI.DashboardEnabled {
		executable, executableErr := os.Executable()
		if executableErr != nil {
			serviceWarning = "automatic restart service could not find the installed executable: " + executableErr.Error()
		} else if serviceErr := bridgeService.Install(executable, configPath); serviceErr != nil {
			serviceWarning = "automatic restart service could not be installed: " + serviceErr.Error()
		} else {
			serviceConfigured = true
		}
	}
	fmt.Println()
	fmt.Println(theme.green(theme.bold("✓ SETUP COMPLETE")))
	fmt.Println("────────────────────────────────────────────────────────────────────────")
	fmt.Printf("Configuration saved to %s\n", configPath)
	if !cfg.CLI.DashboardEnabled {
		fmt.Println("Startup mode: CLI only")
		fmt.Println("The browser dashboard remains available if you enable it later with: local-device-bridge setup")
	} else {
		fmt.Printf("Startup mode: %s\n", experienceLabel(cfg))
		if showPhoneAccess && cfg.Server.AllowLAN {
			fmt.Println(theme.cyan(theme.bold("DASHBOARD LINKS")))
			hostURL := dashboardLocalURL(cfg)
			fmt.Printf("  This computer : %s\n", terminalLink(hostURL, hostURL))
			if err := printPhoneDashboardAccess(configPath, cfg, secrets); err != nil {
				fmt.Println(theme.yellow("Phone dashboard access could not be started: " + err.Error()))
				fmt.Println(theme.dim("You can retry later with: local-device-bridge dashboard phone"))
			}
		} else {
			printDashboardLinks(cfg, theme)
		}
		if cfg.Server.AllowLAN {
			if cfg.Server.InsecureLANHTTP {
				fmt.Println("Phone dashboard: enabled over token-protected HTTP on the trusted LAN")
			} else {
				fmt.Println("Phone dashboard: enabled with HTTPS and browser token protection")
			}
			if cfg.CLI.ShowDashboardURL && !showPhoneAccess {
				fmt.Println("Dashboard token: hidden during setup; run 'local-device-bridge dashboard token' only when a manual sign-in is needed.")
				if !cfg.Server.InsecureLANHTTP {
					fmt.Println("First visit on this Mac: Chrome may show a certificate warning; run 'local-device-bridge dashboard trust' once, then reload.")
				}
			}
		} else {
			fmt.Println("Phone dashboard: disabled; choose 'This computer + my phone' in setup to enable it")
		}
		if cfg.Server.AllowLAN && !showPhoneAccess {
			fmt.Println("Phone link and QR: local-device-bridge dashboard phone")
		}
		if cfg.CLI.AutoLaunchDashboard {
			fmt.Println(theme.dim("Starting the daemon and opening the dashboard..."))
			if err := ensureDaemonAndOpen(configPath, cfg); err != nil {
				fmt.Println(theme.yellow("The setup is saved, but the dashboard could not be opened: " + err.Error()))
				fmt.Println(theme.dim("Start the daemon with: local-device-bridge daemon"))
			} else {
				fmt.Println(theme.green("Dashboard opened in your browser."))
			}
		}
		if serviceConfigured {
			fmt.Println(theme.green("Dashboard service: running automatically after login and restart"))
		} else if serviceWarning != "" {
			fmt.Println(theme.yellow("Dashboard service: " + serviceWarning))
			fmt.Println(theme.dim("The dashboard can still run now, but install the OS service before relying on it after restart."))
		}
	}
	if cfg.Telegram.Enabled && !cfg.Telegram.AllowPublic {
		fmt.Println("Telegram commands: private allowlist only")
	}
	fmt.Println()
	fmt.Println(theme.cyan(theme.bold("RECOMMENDED NEXT STEPS")))
	fmt.Println(theme.dim("The setup wizard has saved your choices. Start with these commands:"))
	fmt.Println()
	fmt.Printf("  %s  %s\n", theme.green("01"), theme.bold("Discover devices"))
	fmt.Println("      local-device-bridge discover")
	fmt.Printf("  %s  %s\n", theme.green("02"), theme.bold("List the inventory"))
	fmt.Println("      local-device-bridge devices list")
	fmt.Printf("  %s  %s\n", theme.green("03"), theme.bold("Pair a supported device"))
	fmt.Println("      local-device-bridge pair <id-or-name>")
	fmt.Printf("  %s  %s\n", theme.green("04"), theme.bold("Open the terminal remote"))
	fmt.Println("      local-device-bridge remote <id-or-name>")
	if cfg.Server.AllowLAN {
		fmt.Println()
		fmt.Println(theme.blue(theme.bold("PHONE ACCESS")))
		if showPhoneAccess {
			fmt.Println("  The phone link and QR code were printed above.")
		} else {
			fmt.Println("  To print the one-time phone link, QR code, and Agent API shortcut:")
			fmt.Println("      local-device-bridge dashboard phone")
		}
		fmt.Println("  Scan the QR, sign in once, then tap Agent API on the phone — no API URL typing is needed.")
	}
	fmt.Println()
	if serviceConfigured {
		fmt.Println(theme.dim("The operating-system service owns the single daemon process; do not also run 'local-device-bridge daemon' in a terminal."))
	} else {
		fmt.Println(theme.dim("The daemon starts when you use dashboard open or run 'local-device-bridge daemon'."))
	}
	fmt.Println()
	fmt.Println("Run local-device-bridge commands for the complete reference.")
	return nil
}

func runService(configPath string, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: local-device-bridge service install|uninstall")
	}
	switch args[0] {
	case "install":
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("find the installed executable: %w", err)
		}
		if err := bridgeService.Install(executable, configPath); err != nil {
			return err
		}
		fmt.Println("local-device-bridge service installed and started")
		return nil
	case "uninstall":
		if err := bridgeService.Uninstall(); err != nil {
			return err
		}
		fmt.Println("local-device-bridge service stopped and removed")
		return nil
	default:
		return errors.New("usage: local-device-bridge service install|uninstall")
	}
}

func printDashboardLinks(cfg config.Config, theme cliTheme) {
	if !cfg.CLI.DashboardEnabled {
		return
	}
	fmt.Println(theme.cyan(theme.bold("DASHBOARD LINKS")))
	hostURL := dashboardLocalURL(cfg)
	fmt.Printf("  This computer : %s\n", terminalLink(hostURL, hostURL))
	if cfg.Server.AllowLAN {
		phoneURL := dashboardLANURL(cfg)
		if cfg.Server.InsecureLANHTTP {
			fmt.Printf("  Phone (same Wi-Fi): %s\n", terminalLink(phoneURL, phoneURL))
		} else {
			fmt.Printf("  Phone (same Wi-Fi, HTTPS): %s\n", terminalLink(phoneURL, phoneURL))
		}
		fmt.Println("  Phone shortcut: scan the QR from 'local-device-bridge dashboard phone', then tap Agent API.")
	} else {
		fmt.Println("  Phone : disabled — rerun setup and choose 'This computer + my phone'")
	}
}

// printPhoneDashboardAccess starts the one configured daemon if necessary and
// prints every phone access option together: a clickable one-time pairing link,
// a QR code for the Camera app, and clear separation between phone access and
// the separate Agent API credential.
func printPhoneDashboardAccess(configPath string, cfg config.Config, secrets *security.SecretStore) error {
	if !cfg.Server.AllowLAN {
		return errors.New("phone dashboard is disabled; run setup and choose 'This computer + my phone'")
	}
	if err := ensureDaemonReady(configPath, cfg, "phone"); err != nil {
		return err
	}
	theme := currentTheme()
	pairURL, err := phonePairingURL(cfg, secrets)
	if err != nil {
		return err
	}
	apiURL := pairURL + "#/api"
	fmt.Println()
	fmt.Println(theme.cyan(theme.bold("PHONE // DASHBOARD ACCESS")))
	fmt.Println(theme.dim("The bridge service is running. The QR and link below sign in one phone automatically:"))
	fmt.Println()
	fmt.Println(theme.bold("OPEN THE PHONE DASHBOARD"))
	if cfg.Server.InsecureLANHTTP {
		fmt.Printf("  Link: %s\n", terminalLink(pairURL, pairURL))
		fmt.Println("  HTTP is token-protected and intended only for a trusted home LAN; it is not encrypted.")
	} else {
		fmt.Printf("  Link: %s\n", terminalLink(pairURL, pairURL))
		fmt.Println("  If the phone shows a certificate warning, use the browser's Advanced → Proceed option on your trusted home Wi-Fi.")
	}
	fmt.Println()
	fmt.Println(theme.bold("OPEN THE AGENT API GUIDE ON THE PHONE"))
	fmt.Printf("  Link: %s\n", terminalLink(apiURL, apiURL))
	fmt.Println("  This opens the Agent API guide after the same automatic phone sign-in.")
	fmt.Println("  For a real AI agent, use the separate Agent API token described below the dashboard guide.")
	fmt.Println()
	fmt.Println(theme.bold("SCAN WITH YOUR PHONE"))
	printPhoneQR(pairURL)
	fmt.Println()
	fmt.Println(theme.dim("No token needs to be typed when this QR/link is used."))
	fmt.Println(theme.dim("The link expires in 10 minutes and works once. Run 'local-device-bridge dashboard phone' again for a new link."))
	fmt.Println()
	fmt.Println(theme.bold("MANUAL PHONE FALLBACK"))
	fmt.Println(theme.dim("Only use this if the QR/link cannot be opened:"))
	fmt.Println("  local-device-bridge dashboard token")
	fmt.Println()
	fmt.Println(theme.cyan(theme.bold("AGENT API // EXTERNAL AGENT ACCESS")))
	fmt.Println(theme.dim("This is separate from phone dashboard access. Only give this token to an AI agent you trust."))
	fmt.Println(theme.dim("Show it when you are ready with: local-device-bridge agent token"))
	return nil
}

func phonePairingURL(cfg config.Config, secrets *security.SecretStore) (string, error) {
	output, err := request(cfg, secrets, http.MethodPost, "/api/v1/auth/pairing-link", nil)
	if err != nil {
		return "", fmt.Errorf("create one-time phone pairing link: %w", err)
	}
	payload, ok := output.(map[string]any)
	if !ok || strings.TrimSpace(fmt.Sprint(payload["token"])) == "" {
		return "", errors.New("daemon returned no one-time phone pairing token; restart the daemon with the current binary")
	}
	base := dashboardLANURL(cfg)
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("pair", strings.TrimSpace(fmt.Sprint(payload["token"])))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// terminalLink uses the standard OSC 8 hyperlink sequence supported by modern
// Terminal, iTerm2, Windows Terminal, and many Linux terminals. Plain output
// remains readable in logs and older terminals.
func terminalLink(label, target string) string {
	if !term.IsTerminal(int(os.Stdout.Fd())) || os.Getenv("NO_COLOR") != "" {
		return label
	}
	return "\x1b]8;;" + target + "\x1b\\" + label + "\x1b]8;;\x1b\\"
}

func printDashboardTokenInstructions(secrets *security.SecretStore) error {
	token, err := api.EnsureAccessToken(secrets)
	if err != nil {
		return err
	}
	theme := currentTheme()
	fmt.Println()
	fmt.Println(theme.cyan(theme.bold("DASHBOARD TOKEN")))
	fmt.Println(theme.dim("Paste this value into the dashboard sign-in box on your phone:"))
	fmt.Printf("  %s\n", theme.bold(token))
	fmt.Println()
	fmt.Println("This unlocks the browser dashboard only. It is different from the Agent API token and Telegram bot token.")
	fmt.Println("It stays the same across restarts and is stored locally in the bridge computer's OS keychain.")
	fmt.Println("Show it again with: local-device-bridge dashboard token")
	fmt.Println()
	return nil
}

func experienceLabel(cfg config.Config) string {
	if !cfg.CLI.DashboardEnabled {
		return "CLI only"
	}
	if cfg.CLI.AutoLaunchDashboard {
		return "CLI + dashboard (opens automatically)"
	}
	return "CLI + dashboard (manual launch)"
}

func dashboardLaunchLabel(enabled bool) string {
	if enabled {
		return "automatic after setup"
	}
	return "manual"
}

func dashboardURLLabel(show bool) string {
	if show {
		return "shown after setup"
	}
	return "hidden"
}

func printStep(number int, title, description string) {
	theme := currentTheme()
	fmt.Println()
	fmt.Printf("%s %s\n", theme.cyan(theme.bold(fmt.Sprintf("STEP %d", number))), theme.bold("//  "+title))
	fmt.Println(strings.Repeat("─", terminalWidth()))
	fmt.Println(theme.dim(description))
}

func splitIDs(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func readSecretInput(reader *bufio.Reader) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		value, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(value)), nil
	}
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	fmt.Println()
	return strings.TrimSpace(value), nil
}

func telegramAccessLabel(public bool) string {
	if public {
		return "public commands (explicitly enabled)"
	}
	return "private allowlist"
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

// dashboardURL is retained as the local URL used by the browser launcher.
// LAN mode must not make the bridge computer open its own phone/LAN address.
func dashboardURL(cfg config.Config) string { return dashboardLocalURL(cfg) }

func dashboardLocalURL(cfg config.Config) string {
	if cfg.Server.AllowLAN {
		return "http://" + api.LocalDashboardBind(cfg.Server.Bind)
	}
	_, port, err := net.SplitHostPort(cfg.Server.Bind)
	if err != nil || port == "" {
		port = "8787"
	}
	return "http://127.0.0.1:" + port
}

func dashboardLANURL(cfg config.Config) string {
	if origin := strings.TrimRight(strings.TrimSpace(cfg.Server.DashboardOrigin), "/"); origin != "" {
		if parsed, err := url.Parse(origin); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
			return origin
		}
	}
	_, port, err := net.SplitHostPort(cfg.Server.Bind)
	if err != nil || port == "" {
		port = "8787"
	}
	host := localIPv4(cfg.Discovery.Interfaces)
	if host == "" {
		host = "<bridge-computer-ip>"
	}
	scheme := "https"
	if cfg.Server.InsecureLANHTTP {
		scheme = "http"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

func dashboardAgentURL(cfg config.Config) string {
	return dashboardLANURL(cfg) + "/#/api"
}

func dashboardLANHTTPURL(cfg config.Config) string {
	copy := cfg
	copy.Server.InsecureLANHTTP = true
	return dashboardLANURL(copy)
}

func dashboardCertificatePath(cfg config.Config) string {
	if cfg.Server.TLSCert != "" {
		return cfg.Server.TLSCert
	}
	return filepath.Join(cfg.State.Directory, "server.crt")
}

func printDashboardTrust(cfg config.Config) {
	if !cfg.Server.AllowLAN || cfg.Server.InsecureLANHTTP {
		fmt.Println("LAN HTTPS is disabled; localhost uses HTTP and does not need a certificate.")
		return
	}
	path := dashboardCertificatePath(cfg)
	fmt.Printf("Generated dashboard certificate: %s\n", path)
	fmt.Println("The first LAN visit may show a browser certificate warning because this is a private self-signed certificate.")
	switch runtime.GOOS {
	case "darwin":
		fmt.Printf("To trust it on this Mac (explicit home-LAN action):\n  security add-trusted-cert -d -r trustRoot -k %q %q\n", filepath.Join(os.Getenv("HOME"), "Library/Keychains/login.keychain-db"), path)
	case "windows":
		fmt.Printf("To trust it for the current Windows user:\n  certutil -user -addstore Root %q\n", path)
	default:
		fmt.Printf("To trust it on Debian/Ubuntu:\n  sudo cp %q /usr/local/share/ca-certificates/local-device-bridge.crt && sudo update-ca-certificates\n", path)
	}
	fmt.Println("On a phone, use the browser's Advanced → Proceed option on your trusted home Wi-Fi, or install the certificate into the phone's trusted certificate store.")
}

func localIPv4(preferredInterfaces []string) string {
	if len(preferredInterfaces) > 0 {
		for _, name := range preferredInterfaces {
			if address := interfaceIPv4(strings.TrimSpace(name)); address != "" {
				return address
			}
		}
	}
	// Ask the OS routing table which interface would carry normal traffic. This
	// avoids printing a VPN, Docker, or stale adapter address just because it
	// happened to be returned first by net.Interfaces.
	if connection, err := net.Dial("udp4", "1.1.1.1:53"); err == nil {
		if address, ok := connection.LocalAddr().(*net.UDPAddr); ok && address.IP.To4() != nil && !address.IP.IsLoopback() {
			_ = connection.Close()
			return address.IP.To4().String()
		}
		_ = connection.Close()
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if address := interfaceIPv4(iface.Name); address != "" {
			return address
		}
	}
	return ""
}

func interfaceIPv4(name string) string {
	iface, err := net.InterfaceByName(name)
	if err != nil || iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
		return ""
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, address := range addresses {
		var ip net.IP
		switch value := address.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if ip != nil && ip.To4() != nil && !ip.IsLinkLocalUnicast() {
			return ip.To4().String()
		}
	}
	return ""
}

func dashboardEndpoint(cfg config.Config) string {
	host, port, err := net.SplitHostPort(cfg.Server.Bind)
	if err != nil {
		return cfg.Server.Bind
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func dashboardScheme(cfg config.Config) string {
	if cfg.Server.AllowLAN && !cfg.Server.InsecureLANHTTP {
		return "https"
	}
	return "http"
}

func dashboardHealth(cfg config.Config) bool {
	endpoint := dashboardScheme(cfg) + "://" + dashboardEndpoint(cfg) + "/api/v1/health"
	requestContext, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	client := &http.Client{}
	if cfg.Server.AllowLAN && !cfg.Server.InsecureLANHTTP {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func dashboardLocalHealth(cfg config.Config) bool {
	endpoint := dashboardLocalURL(cfg) + "/api/v1/health"
	requestContext, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	response, err := (&http.Client{}).Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func ensureDaemonAndOpen(configPath string, cfg config.Config) error {
	return ensureDaemonAndOpenURL(configPath, cfg, dashboardLocalURL(cfg), "Mac")
}

func ensureDaemonAndOpenURL(configPath string, cfg config.Config, url string, destination string) error {
	if err := ensureDaemonReady(configPath, cfg, destination); err != nil {
		return err
	}
	if cfg.Server.AllowLAN && destination == "phone" {
		if cfg.Server.InsecureLANHTTP {
			fmt.Println("Opening the phone dashboard over token-protected HTTP on the trusted LAN.")
		} else {
			fmt.Println("Opening the phone dashboard over HTTPS. The phone may need Advanced → Proceed or the generated certificate installed.")
		}
	} else if cfg.Server.AllowLAN {
		fmt.Println("Opening the Mac dashboard over localhost HTTP; the Phone link is printed separately.")
	}
	return openDashboard(url)
}

func ensureDaemonReady(configPath string, cfg config.Config, destination string) error {
	ready := dashboardHealth(cfg)
	if destination == "Mac" && cfg.Server.AllowLAN {
		ready = dashboardLocalHealth(cfg)
	}
	if !ready {
		// Repair the supervisor before falling back to a detached process. This
		// makes `dashboard open` recover installations whose launch agent or
		// user service was removed, while the daemon lock still guarantees one
		// process if an older manual daemon is present.
		if cfg.CLI.DashboardEnabled {
			_ = bridgeService.Install(mustExecutable(), configPath)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			ready = dashboardHealth(cfg)
			if destination == "Mac" && cfg.Server.AllowLAN {
				ready = dashboardLocalHealth(cfg)
			}
			if ready {
				break
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	if !ready {
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("find the installed executable: %w", err)
		}
		command := exec.Command(executable, "--config", configPath, "daemon")
		command.Stdin = nil
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		if err := command.Start(); err != nil {
			return fmt.Errorf("start the daemon: %w", err)
		}
		_ = command.Process.Release()
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			ready = dashboardHealth(cfg)
			if destination == "Mac" && cfg.Server.AllowLAN {
				ready = dashboardLocalHealth(cfg)
			}
			if ready {
				break
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	ready = dashboardHealth(cfg)
	if destination == "Mac" && cfg.Server.AllowLAN {
		ready = dashboardLocalHealth(cfg)
	}
	if !ready {
		return errors.New("the daemon did not become ready; run local-device-bridge daemon and try again")
	}
	return nil
}

func mustExecutable() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	return executable
}

func openDashboard(url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return fmt.Errorf("open %s: %w", url, err)
	}
	return nil
}

func runTV(cfg config.Config, secrets *security.SecretStore, args []string) error {
	if len(args) == 2 && strings.EqualFold(args[1], "help") {
		output, err := request(cfg, secrets, http.MethodGet, deviceAPIPath(args[0], "/guide"), nil)
		if err != nil {
			return err
		}
		renderDeviceGuide(output)
		return nil
	}
	if len(args) < 2 {
		return errors.New("usage: local-device-bridge device <id-or-name> help | rename <friendly name> | <status|on|off|volume-up [steps]|volume-down [steps]|volume|mute|key|source|channel>")
	}
	id, operation := args[0], strings.ToLower(args[1])
	if operation == "rename" {
		if len(args) < 3 {
			return errors.New("usage: local-device-bridge device <id-or-name> rename <friendly name>")
		}
		return call(cfg, secrets, http.MethodPost, deviceAPIPath(id, "/name"), map[string]any{"name": strings.Join(args[2:], " ")}, true)
	}
	action := map[string]core.Action{"status": core.ActionStatus, "on": core.ActionPowerOn, "off": core.ActionPowerOff, "volume-up": core.ActionVolumeUp, "volume-down": core.ActionVolumeDown, "volume": core.ActionVolumeSet, "volume-set": core.ActionVolumeSet, "mute": core.ActionMute, "key": core.ActionKey, "source": core.ActionSource, "channel": core.ActionChannel}[operation]
	if action == "" {
		return fmt.Errorf("unsupported TV operation %q", operation)
	}
	arguments := map[string]string{}
	relativeVolume := false
	if len(args) >= 3 {
		// Make the common form `device <id> volume +3` useful: a signed
		// volume value means relative steps, while an unsigned value remains
		// the adapter's absolute-volume operation.
		if action == core.ActionVolumeSet && (strings.HasPrefix(args[2], "+") || strings.HasPrefix(args[2], "-")) {
			if strings.HasPrefix(args[2], "+") {
				action = core.ActionVolumeUp
			} else {
				action = core.ActionVolumeDown
			}
			arguments["steps"] = strings.TrimLeft(args[2], "+-")
			relativeVolume = true
		}
		if action == core.ActionKey {
			arguments["key"] = args[2]
		}
		if action == core.ActionVolumeSet {
			arguments["volume"] = args[2]
		}
		if action == core.ActionSource {
			arguments["source"] = args[2]
		}
		if action == core.ActionChannel {
			arguments["channel"] = args[2]
		}
		if (action == core.ActionVolumeUp || action == core.ActionVolumeDown) && !relativeVolume {
			arguments["steps"] = args[2]
		}
	}
	if (action == core.ActionVolumeUp || action == core.ActionVolumeDown) && len(args) >= 3 {
		if steps, parseErr := strconv.Atoi(args[2]); parseErr != nil || steps < 1 || steps > 20 {
			return errors.New("volume steps must be a number from 1 to 20")
		}
	}
	if (action == core.ActionKey || action == core.ActionVolumeSet || action == core.ActionSource || action == core.ActionChannel) && len(arguments) == 0 {
		return errors.New("this operation requires an argument")
	}
	body := map[string]any{"action": action, "arguments": arguments}
	return call(cfg, secrets, http.MethodPost, deviceAPIPath(id, "/commands"), body, true)
}

func renderDeviceGuide(output any) {
	payload, _ := output.(map[string]any)
	device, _ := payload["device"].(map[string]any)
	name := displayValue(device)
	fmt.Printf("DEVICE COMMANDS · %s\n", name)
	fmt.Printf("  platform: %s %s · address: %s · paired: %s\n", value(device, "manufacturer", "Unknown"), value(device, "model", value(device, "kind", "unknown")), value(device, "ip", "not reported"), value(device, "paired", "false"))
	if ready, ok := payload["ready"].(bool); ok {
		fmt.Printf("  ready: %s\n", enabledLabel(ready))
	}
	if steps, ok := payload["steps"].([]any); ok && len(steps) > 0 {
		fmt.Println("\nSETUP / ACCESS")
		for index, step := range steps {
			fmt.Printf("  %d. %s\n", index+1, fmt.Sprint(step))
		}
	}
	capabilities, _ := device["capabilities"].([]any)
	if len(capabilities) > 0 {
		fmt.Println("\nAVAILABLE ACTIONS")
		for _, capability := range capabilities {
			fmt.Printf("  • %s\n", fmt.Sprint(capability))
		}
	}
	ref := displayValue(device)
	fmt.Printf("\nExamples: device %q status · device %q key HOME · device %q volume +3\n", ref, ref, ref)
}

func deviceAPIPath(id, suffix string) string {
	return "/api/v1/devices/" + url.PathEscape(id) + suffix
}

func runMac(cfg config.Config, secrets *security.SecretStore, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: local-device-bridge mac <id-or-name> <status|wake|sleep>")
	}
	action := map[string]core.Action{"status": core.ActionStatus, "wake": core.ActionPowerOn, "sleep": core.ActionPowerOff}[strings.ToLower(args[1])]
	if action == "" {
		return fmt.Errorf("unsupported Mac operation %q", args[1])
	}
	return call(cfg, secrets, http.MethodPost, deviceAPIPath(args[0], "/commands"), map[string]any{"action": action}, true)
}

func runRemote(cfg config.Config, secrets *security.SecretStore, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: local-device-bridge remote <supported-tv-id-or-name>")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("remote needs an interactive terminal; use device <device-id> key <KEY> for scripts")
	}
	output, err := request(cfg, secrets, http.MethodGet, "/api/v1/devices", nil)
	if err != nil {
		return err
	}
	device, err := findDevice(output, args[0])
	if err != nil {
		return err
	}
	manufacturer := strings.ToLower(fmt.Sprint(device["manufacturer"]))
	if strings.ToLower(fmt.Sprint(device["kind"])) != string(core.DeviceKindTV) || !hasCapability(device, string(core.CapabilityNavigation)) {
		return errors.New("remote currently supports paired TVs with navigation capabilities")
	}
	if manufacturer == "samsung" {
		if paired, _ := device["paired"].(bool); !paired {
			return errors.New("this TV is not paired; run pair <id-or-name>, accept the prompt once, then start remote again")
		}
	}

	printBanner()
	name := value(device, "name", "Samsung TV")
	fmt.Printf("REMOTE SESSION  //  %s\n", name)
	fmt.Println("────────────────────────────────────────────────────────────────────────")
	fmt.Println("Arrows navigate  Enter select  +/- volume  M mute  Space play/pause")
	fmt.Println("H home  B back  S source  W wake  O power off  Q quit")
	fmt.Println()
	fmt.Print("READY  > ")

	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("enable terminal remote mode: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), state)

	for {
		key, err := readRemoteKey(os.Stdin)
		if err != nil {
			return fmt.Errorf("read terminal input: %w", err)
		}
		if key == "quit" {
			fmt.Fprintln(os.Stdout, "\r\nREMOTE CLOSED")
			return nil
		}
		if key == "" {
			continue
		}
		var action core.Action
		arguments := map[string]string{}
		switch key {
		case "UP", "DOWN", "LEFT", "RIGHT", "ENTER", "RETURN", "HOME", "PLAY":
			action = core.ActionKey
			arguments["key"] = key
		case "VOLUME_UP":
			action = core.ActionVolumeUp
		case "VOLUME_DOWN":
			action = core.ActionVolumeDown
		case "MUTE":
			action = core.ActionMute
		case "SOURCE":
			action = core.ActionSource
			arguments["source"] = "source"
		case "POWER_ON":
			action = core.ActionPowerOn
		case "POWER_OFF":
			action = core.ActionPowerOff
		default:
			continue
		}
		result, err := request(cfg, secrets, http.MethodPost, deviceAPIPath(args[0], "/commands"), map[string]any{"action": action, "arguments": arguments})
		if err != nil {
			fmt.Fprintf(os.Stdout, "\r\x1b[2KERROR  %s\nREADY  > ", err.Error())
			continue
		}
		message := "Command sent"
		if payload, ok := result.(map[string]any); ok {
			if text, ok := payload["message"].(string); ok && text != "" {
				message = text
			}
		}
		fmt.Fprintf(os.Stdout, "\r\x1b[2KOK     %s\nREADY  > ", message)
	}
}

func readRemoteKey(reader io.Reader) (string, error) {
	var first [1]byte
	if _, err := reader.Read(first[:]); err != nil {
		return "", err
	}
	switch first[0] {
	case 3, 'q', 'Q':
		return "quit", nil
	case 13, 10:
		return "ENTER", nil
	case ' ', 'p', 'P':
		return "PLAY", nil
	case 'm', 'M':
		return "MUTE", nil
	case '+', '=':
		return "VOLUME_UP", nil
	case '-', '_':
		return "VOLUME_DOWN", nil
	case 'h', 'H':
		return "HOME", nil
	case 'b', 'B':
		return "RETURN", nil
	case 's', 'S':
		return "SOURCE", nil
	case 'w', 'W':
		return "POWER_ON", nil
	case 'o', 'O':
		return "POWER_OFF", nil
	case 27:
		var sequence [2]byte
		if _, err := io.ReadFull(reader, sequence[:]); err != nil {
			return "", err
		}
		if sequence[0] != '[' && sequence[0] != 'O' {
			return "", nil
		}
		switch sequence[1] {
		case 'A':
			return "UP", nil
		case 'B':
			return "DOWN", nil
		case 'C':
			return "RIGHT", nil
		case 'D':
			return "LEFT", nil
		}
	}
	return "", nil
}

func call(cfg config.Config, secrets *security.SecretStore, method, path string, body any, human bool) error {
	output, err := requestWithProgress(cfg, secrets, method, path, body)
	if err != nil {
		return err
	}
	if human {
		render(path, output)
	} else {
		b, _ := json.MarshalIndent(output, "", "  ")
		fmt.Println(string(b))
	}
	return nil
}

func request(cfg config.Config, secrets *security.SecretStore, method, path string, body any) (any, error) {
	token, err := api.EnsureAccessToken(secrets)
	if err != nil {
		return nil, err
	}
	endpoint := dashboardScheme(cfg) + "://" + dashboardEndpoint(cfg) + path
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	requestContext, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	if cfg.Server.AllowLAN && !cfg.Server.InsecureLANHTTP {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("daemon unavailable at %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	var output any
	if err := json.NewDecoder(resp.Body).Decode(&output); err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		if value, ok := output.(map[string]any); ok {
			if message, ok := value["error"].(string); ok {
				return nil, errors.New(message)
			}
		}
		return nil, errors.New("request failed")
	}
	return output, nil
}

func findDevice(output any, id string) (map[string]any, error) {
	payload, ok := output.(map[string]any)
	if !ok {
		return nil, errors.New("daemon returned an invalid device inventory")
	}
	items, ok := payload["devices"].([]any)
	if !ok {
		return nil, errors.New("daemon returned no device inventory")
	}
	for _, raw := range items {
		device, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprint(device["id"]) == id || strings.EqualFold(fmt.Sprint(device["id"]), id) || strings.EqualFold(fmt.Sprint(device["alias"]), id) || strings.EqualFold(fmt.Sprint(device["name"]), id) {
			return device, nil
		}
	}
	return nil, fmt.Errorf("device %q was not found; run discover first", id)
}

func render(path string, output any) {
	if strings.HasSuffix(path, "/discovery/scan") || strings.HasSuffix(path, "/devices") {
		renderInventory(output)
		return
	}
	payload, _ := output.(map[string]any)
	if paired, ok := payload["paired"].(bool); ok && paired {
		fmt.Println("Device paired successfully. You can now use its supported commands.")
		return
	}
	if unpaired, ok := payload["paired"].(bool); ok && !unpaired && strings.HasSuffix(path, "/unpair") {
		fmt.Println("Device unpaired. Pairing controls are available again.")
		return
	}
	if device, ok := payload["device"].(map[string]any); ok && strings.HasSuffix(path, "/name") {
		fmt.Printf("Device renamed to %s. You can now use that name in commands.\n", displayValue(device))
		return
	}
	if message, ok := payload["message"].(string); ok {
		fmt.Println(message)
		return
	}
	b, _ := json.MarshalIndent(output, "", "  ")
	fmt.Println(string(b))
}

func displayValue(device map[string]any) string {
	if alias := value(device, "alias", ""); alias != "" {
		return alias
	}
	return value(device, "name", "Unnamed device")
}

func renderInventory(output any) {
	payload, _ := output.(map[string]any)
	items, _ := payload["devices"].([]any)
	found := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		if device, ok := raw.(map[string]any); ok {
			if metadata, nested := device["metadata"].(map[string]any); nested {
				device = metadata
			}
			found = append(found, device)
		}
	}
	if len(found) == 0 {
		fmt.Println("No devices found yet. Run discover or scan the network.")
		return
	}
	groups := map[string][]map[string]any{}
	for _, device := range found {
		group := inventoryGroup(device)
		groups[group] = append(groups[group], device)
	}
	order := []string{"TVs & displays", "Game consoles", "Mac OS", "Windows", "Raspberry Pi"}
	visible := 0
	for _, device := range found {
		if inventoryGroup(device) != "" {
			visible++
		}
	}
	fmt.Printf("INVENTORY · %d device(s)\n", visible)
	for _, group := range order {
		items := groups[group]
		if len(items) == 0 {
			continue
		}
		fmt.Printf("\n%s (%d)\n", group, len(items))
		for _, device := range items {
			status := "offline"
			if strings.EqualFold(value(device, "online", "false"), "true") {
				status = "online"
			}
			setup := "discovery only"
			if strings.EqualFold(value(device, "paired", "false"), "true") {
				setup = "paired"
			} else if value(device, "kind", "unknown") != "unknown" {
				setup = "setup available"
			}
			fmt.Printf("  %s %s — %s — %s — %s — %s\n", statusMarker(status), displayValue(device), value(device, "ip", "no IP"), value(device, "model", value(device, "kind", "unknown")), setup, status)
			fmt.Printf("      id: %s\n", value(device, "id", "unknown-id"))
			if alias := value(device, "alias", ""); alias != "" {
				fmt.Printf("      discovered as: %s\n", value(device, "name", "unnamed"))
			}
		}
	}
}

func statusMarker(status string) string {
	if status == "online" {
		return "●"
	}
	return "○"
}

func inventoryGroup(device map[string]any) string {
	category := strings.ToLower(value(device, "category", ""))
	switch category {
	case "tv_display":
		return "TVs & displays"
	case "console":
		return "Game consoles"
	case "computer":
		platform := strings.ToLower(value(device, "platform", ""))
		switch platform {
		case "macos":
			return "Mac OS"
		case "windows laptop", "windows":
			return "Windows"
		case "raspberry pi":
			return "Raspberry Pi"
		}
	}
	manufacturer := strings.ToLower(value(device, "manufacturer", ""))
	model := strings.ToLower(value(device, "model", ""))
	name := strings.ToLower(value(device, "name", ""))
	kind := strings.ToLower(value(device, "kind", ""))
	text := manufacturer + " " + model + " " + name + " " + kind
	switch {
	case manufacturer == "samsung":
		return "TVs & displays"
	case manufacturer == "apple" || strings.Contains(text, "macos") || strings.Contains(text, "macbook") || strings.Contains(text, "mac mini") || strings.Contains(text, "mac-mini"):
		return "Mac OS"
	case strings.Contains(text, "raspberry pi") || strings.Contains(text, "raspbian"):
		return "Raspberry Pi"
	case strings.Contains(text, "windows") || strings.Contains(text, "win32") || strings.Contains(text, "microsoft"):
		return "Windows"
	case kind == "monitor" || kind == "display" || kind == "tv":
		return "TVs & displays"
	default:
		return ""
	}
}

func value(device map[string]any, key, fallback string) string {
	if value, ok := device[key]; ok {
		return fmt.Sprint(value)
	}
	return fallback
}

func hasCapability(device map[string]any, want string) bool {
	values, ok := device["capabilities"].([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(fmt.Sprint(value), want) {
			return true
		}
	}
	return false
}

func visibilityLabel(enabled bool) string {
	if enabled {
		return "shown"
	}
	return "hidden"
}

func printBanner() {
	printBannerStyled(os.Stdout, terminalWidth(), currentTheme())
}

func printBannerWidth(writer io.Writer, width int) {
	printBannerStyled(writer, width, cliTheme{})
}

func printBannerStyled(writer io.Writer, width int, theme cliTheme) {
	if width < 40 {
		width = 40
	}
	rule := strings.Repeat("-", width)
	fmt.Fprintln(writer, theme.dim(rule))
	if width < 90 {
		fmt.Fprintln(writer, theme.gradient(centerText("LOCAL DEVICE BRIDGE", width)))
		fmt.Fprintln(writer, theme.blue(centerText("CLI  //  SETUP", width)))
		fmt.Fprintln(writer, theme.dim(centerText("Discover  •  Pair  •  Control  •  Audit", width)))
		fmt.Fprintln(writer, theme.dim(rule))
		return
	}
	fmt.Fprintln(writer, theme.dim(centerText("╭───◆  CONTROL NODE  ◆───╮", width)))
	for _, line := range wordmarkRowsForWidth(width, "LOCAL DEVICE BRIDGE") {
		fmt.Fprintln(writer, theme.gradient(centerText(line, width)))
	}
	fmt.Fprintln(writer, theme.dim(centerText("╰───◆  LOCAL NETWORK AUTOMATION  ◆───╯", width)))
	fmt.Fprintln(writer, theme.blue(centerText("CLI  //  SETUP", width)))
	fmt.Fprintln(writer, theme.dim(centerText("Discover  •  Pair  •  Control  •  Audit", width)))
	fmt.Fprintln(writer, theme.dim(rule))
}

func (theme cliTheme) gradient(value string) string {
	if !theme.Enabled || value == "" {
		return value
	}
	runes := []rune(value)
	var output strings.Builder
	for index, character := range runes {
		if character == ' ' {
			output.WriteRune(character)
			continue
		}
		progress := float64(index) / float64(len(runes)-1)
		red := int(14 + 145*progress)
		green := int(211 - 119*progress)
		blue := int(255 - 3*progress)
		fmt.Fprintf(&output, "\x1b[1;38;2;%d;%d;%dm%c\x1b[0m", red, green, blue, character)
	}
	return output.String()
}

func centerText(value string, width int) string {
	padding := (width - len([]rune(value))) / 2
	if padding < 0 {
		return value
	}
	return strings.Repeat(" ", padding) + value
}

func wordmarkRowsForWidth(width int, words ...string) []string {
	if width >= 90 {
		return wordmarkRowsWithFont(wideWordmarkFont(), words...)
	}
	return wordmarkRowsWithFont(compactWordmarkFont(), words...)
}

func compactWordmarkFont() map[rune][]string {
	return map[rune][]string{
		'A': {" █ ", "█ █", "███", "█ █", "█ █"},
		'B': {"██ ", "█ █", "██ ", "█ █", "██ "},
		'C': {"███", "█  ", "█  ", "█  ", "███"},
		'D': {"██ ", "█ █", "█ █", "█ █", "██ "},
		'E': {"███", "█  ", "██ ", "█  ", "███"},
		'G': {"███", "█  ", "█ █", "█ █", "███"},
		'I': {"███", " █ ", " █ ", " █ ", "███"},
		'L': {"█  ", "█  ", "█  ", "█  ", "███"},
		'O': {"███", "█ █", "█ █", "█ █", "███"},
		'R': {"██ ", "█ █", "██ ", "█ █", "█ █"},
		'V': {"█ █", "█ █", "█ █", " █ ", " █ "},
	}
}

func wideWordmarkFont() map[rune][]string {
	return map[rune][]string{
		'A': {" ██ ", "█  █", "████", "█  █", "█  █"},
		'B': {"███ ", "█  █", "███ ", "█  █", "███ "},
		'C': {" ██ ", "█   ", "█   ", "█   ", " ██ "},
		'D': {"███ ", "█  █", "█  █", "█  █", "███ "},
		'E': {"████", "█   ", "███ ", "█   ", "████"},
		'G': {" ██ ", "█   ", "█ ██", "█  █", " ███"},
		'I': {"████", " ██ ", " ██ ", " ██ ", "████"},
		'L': {"█   ", "█   ", "█   ", "█   ", "████"},
		'O': {" ██ ", "█  █", "█  █", "█  █", " ██ "},
		'R': {"███ ", "█  █", "███ ", "█ █ ", "█  █"},
		'V': {"█  █", "█  █", "█  █", " ██ ", " ██ "},
	}
}

func wordmarkRowsWithFont(font map[rune][]string, words ...string) []string {
	rows := make([]strings.Builder, 5)
	for wordIndex, word := range words {
		if wordIndex > 0 {
			for row := range rows {
				rows[row].WriteString("   ")
			}
		}
		for charIndex, character := range word {
			if character == ' ' {
				for row := range rows {
					rows[row].WriteString("    ")
				}
				continue
			}
			glyph, ok := font[character]
			if !ok {
				continue
			}
			for row := range rows {
				if charIndex > 0 && word[charIndex-1] != ' ' {
					rows[row].WriteByte(' ')
				}
				rows[row].WriteString(glyph[row])
			}
		}
	}
	result := make([]string, len(rows))
	for index := range rows {
		result[index] = rows[index].String()
	}
	return result
}

func usage() {
	printBanner()
	fmt.Print(`local-device-bridge

Usage:
  local-device-bridge                         open the interactive CLI home
  local-device-bridge cli                     open the interactive CLI home
  local-device-bridge setup                   create config and setup guidance
  local-device-bridge daemon                  run the local service
  local-device-bridge service install         start at login and restart after failure
  local-device-bridge service uninstall       stop and remove the automatic service
  local-device-bridge discover                scan the local network
  local-device-bridge devices list            list known devices
  local-device-bridge devices rename <ref> <name>
                                               save a friendly device name
  local-device-bridge pair <ref>              pair a TV or remote Mac; prompt for a Mac account privately
  local-device-bridge unpair <ref>            remove saved pairing
  local-device-bridge mac <ref> status|wake|sleep
                                               read/control a remote Mac
	local-device-bridge device <ref> ...        run a normalized device command
  local-device-bridge tv <ref> help           show setup and actions
  local-device-bridge remote <ref>            open the interactive TV remote
  local-device-bridge commands                 explain every CLI command
  local-device-bridge agent manifest|openapi  print the agent API contract
  local-device-bridge agent guide <ref>       print pairing guidance
  local-device-bridge agent token             print the separate agent token
  local-device-bridge dashboard token         show the browser token
  local-device-bridge dashboard open          start/open the host dashboard
  local-device-bridge dashboard phone         show one-time phone link and QR
  local-device-bridge dashboard cert|trust    show certificate information

<ref> may be a stable device ID, discovered name, or saved friendly alias.
Setup uses arrow keys and Enter in a terminal, with numbered/plain fallback for pipes.
`)
}

func printCommands() {
	printBanner()

	fmt.Print(`LOCAL DEVICE BRIDGE // COMMAND CENTER

START HERE
  local-device-bridge              open the interactive home
  local-device-bridge discover     scan the local network
  local-device-bridge devices list list the saved inventory
  local-device-bridge commands     show this reference

PHONE // DASHBOARD ACCESS
  local-device-bridge dashboard phone
    Print a one-time phone link and QR code. Scan it with the phone camera;
    the browser signs in automatically. The link expires after 10 minutes.
  local-device-bridge dashboard token
    Show the manual browser token fallback. This is not an Agent API token.

DEVICES // FRIENDLY NAMES
  devices rename <device> "Living Room TV"
  device "Living Room TV" status
  pair "Living Room TV"                 pair a TV, or privately enter a Mac account when prompted
  unpair "Living Room TV"
  <device> may be an ID, discovered name, or saved friendly name.

REMOTE // COMMON ACTIONS
  device <device> on|off
  device <device> volume-up [steps]       default: 1, max: 20
  device <device> volume-down [steps]
  device <device> volume +3|-3|20         relative or absolute volume
  device <device> mute
  device <device> key HOME|UP|DOWN|LEFT|RIGHT|ENTER|PLAYPAUSE
  device <device> source <NAME>
  device <device> channel <NUMBER>
  remote <device>                         interactive arrow-key remote
  tv <device> help                        device-specific setup and actions

CONSOLES // SAFE LOCAL SUPPORT
  device <console> status                 show the latest LAN discovery state
  device <console> on                     send Wake-on-LAN when a MAC is known
  device <console> off                    explain why universal power-off is unavailable
  Consoles do not use bridge pairing. Enable network wake on the console and
  use the official PlayStation, Xbox, or Nintendo app for account-backed control.

PAIRING
  pair <device>                            follow the device guide
  unpair <device>                          remove saved credentials
  agent guide <device>                     show exact pairing steps

AGENT API // EXTERNAL AI ACCESS
  agent token                              print the separate bearer token
  agent manifest                           print machine-readable capabilities
  agent openapi                            print the OpenAPI contract
  Give the token only to a trusted local agent through its private secrets.
  Tell it: "Use the local bridge, read the manifest and device guide first,
  ask before pairing or power commands, and use my friendly device names."

OTHER
  dashboard open                           start/open the configured dashboard
  dashboard cert|trust                     certificate information
  mac <device> status|wake|sleep           supported remote Mac actions

The bridge host is shown for status only. Its wake and sleep controls are hidden
and blocked so the dashboard cannot disable the computer running the bridge.
`)
}
