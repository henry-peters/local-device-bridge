const login = document.querySelector('#login');
const app = document.querySelector('#app');
const devices = document.querySelector('#devices');
const homeView = document.querySelector('#home-view');
const deviceView = document.querySelector('#device-view');
const commandView = document.querySelector('#command-view');
const settingsView = document.querySelector('#settings-view');
const apiView = document.querySelector('#api-view');
const commandList = document.querySelector('#command-list');
const apiContent = document.querySelector('#api-content');
const detailContent = document.querySelector('#detail-content');
const deviceCount = document.querySelector('#device-count');
const detailState = document.querySelector('#detail-state');
const scanButton = document.querySelector('#scan');
const headerActions = document.querySelector('#header-actions');
const bridgeContext = document.querySelector('#bridge-context');
const bridgeContextMark = document.querySelector('#bridge-context .bridge-context-mark');
const bridgeContextTitle = document.querySelector('#bridge-context-title');
const bridgeContextLabel = document.querySelector('#bridge-context-label');
const bridgeContextDescription = document.querySelector('#bridge-context-description');

let deviceMap = new Map();
let activeDeviceId = null;
let renderSerial = 0;
let lastRouteKey = '';
let refreshInFlight = false;
let scanInFlight = false;
let commandEvents = [];
let commandFilter = 'all';
let commandSearch = '';
let apiManifest = null;
const autoScanAttempts = new Set();
const collapsedSections = new Set();
const lastCommandSucceeded = new Map();
let initialDiscoveryRequested = false;

function setAuthenticatedUI(authenticated) {
  if (headerActions) headerActions.hidden = !authenticated;
}

function setBridgeContext(mode, mark, label, title, description) {
  if (!bridgeContext) return;
  bridgeContext.className = `bridge-context ${mode}`;
  bridgeContextMark.textContent = mark;
  bridgeContextLabel.textContent = label;
  bridgeContextTitle.textContent = title;
  bridgeContextDescription.textContent = description;
}

async function checkBridgeContext() {
  if (!bridgeContext) return;
  try {
    const response = await fetch('/api/v1/health', {cache: 'no-store'});
    if (!response.ok) throw new Error('Bridge health check failed');
    const hostname = String(window.location.hostname || '').toLowerCase();
    const isLoopback = hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '::1' || hostname === '[::1]';
    if (isLoopback) {
      setBridgeContext('is-ready', '✓', 'BRIDGE HOST', 'Bridge host detected', 'This browser is on the computer running local-device-bridge. Localhost access is automatic.');
    } else {
      setBridgeContext('is-client', '↗', 'LAN CLIENT', 'Phone / LAN dashboard connected', 'This browser reached the bridge over your local network. A QR pairing link signs it in automatically; then use the controls or Agent API.');
    }
  } catch (_) {
    setBridgeContext('is-error', '!', 'CHECK FAILED', 'Bridge service needs attention', 'The page loaded, but the bridge health check did not complete. Confirm the daemon is running and reload this page.');
  }
}

async function autoPairBrowser() {
  const params = new URLSearchParams(window.location.search);
  const pairingToken = params.get('pair');
  if (!pairingToken) return false;
  try {
    await fetch('/api/v1/auth/pair', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({token: pairingToken}),
    }).then(async response => {
      if (!response.ok) {
        const body = await response.text();
        throw new Error(body || 'Phone pairing link was rejected');
      }
      return response.json();
    });
    // Remove the one-time credential immediately so it is not left in the
    // address bar, browser history, screenshots, or copied links.
    window.history.replaceState({}, document.title, window.location.pathname + window.location.hash);
    return true;
  } catch (error) {
    window.history.replaceState({}, document.title, window.location.pathname + window.location.hash);
    const loginError = document.querySelector('#login-error');
    if (loginError) loginError.textContent = 'This phone link has expired or was already used. Run local-device-bridge dashboard phone again and scan the new QR code.';
    return false;
  }
}

async function api(path, options = {}) {
  const response = await fetch(path, {headers: {'Content-Type': 'application/json', ...(options.headers || {})}, ...options});
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || 'Request failed');
  return body;
}

function setFeedback(message = '', error = false) {
  const feedback = document.querySelector('#command-feedback');
  if (!feedback) return;
  const reserved = feedback.classList.contains('remote-feedback');
  feedback.hidden = reserved ? false : !message;
  feedback.textContent = message;
  feedback.classList.toggle('feedback-error', error);
  feedback.classList.toggle('is-visible', Boolean(message));
}

function formatDate(value) {
  if (!value) return 'Not reported';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? 'Not reported' : date.toLocaleString();
}

function friendlyError(message) {
  const text = String(message || 'Something went wrong.');
  if (/local control was not verified/i.test(text)) {
    return 'Samsung pairing is not available yet. Wake the TV, enable its network remote settings, and scan again so the bridge can verify the real local control service.';
  }
  if (/wake-on-lan was sent.*did not become reachable|did not become reachable within/i.test(text)) {
    return 'Wake-on-LAN was sent, but the TV did not come back online. Enable network standby or Wake-on-WLAN on the TV, keep it on the same LAN, and try Wake TV again.';
  }
  if (/no route to host|network is unreachable|timed out|i\/o timeout/i.test(text)) {
    return 'The bridge cannot reach this device. Make sure it is awake, on the same non-guest network, and then select Scan network.';
  }
  if (/connection refused|rejected the local api/i.test(text)) {
    return 'The device is reachable, but its local control service refused the connection. Enable its network remote setting, then try again.';
  }
  if (/network remote setting|remote access unavailable/i.test(text)) {
    return 'The device is paired, but local remote access is disabled. Enable the device network-remote setting and pair it again.';
  }
  if (/requested pairing approval again/i.test(text)) {
    return 'Samsung asked for approval again, so the old pairing was cleared. Open Access Rules, choose Pair TV, accept one prompt, and wait for Paired before using the remote.';
  }
  if (/pairing expired|saved pairing was cleared|pair again/i.test(text)) {
    return 'The TV rejected its saved pairing. The old credential was cleared; pair the TV again and accept the prompt.';
  }
  if (/pairing.*required|pair the tv/i.test(text)) {
    return 'Pair this device first, then try the control again.';
  }
  if (/Mac pairing failed/i.test(text) && /permission denied|publickey|authentication/i.test(text)) {
    return 'The bridge reached the Mac, but its SSH key was not accepted. Run the “Give the bridge a secure login” command shown above, then pair again.';
  }
  if (/Mac pairing failed/i.test(text) && /sudo|pmset|password/i.test(text)) {
    return 'SSH is ready, but the restricted Mac power permission is missing. Run the “Allow only Mac power actions” command on the target Mac, then pair again.';
  }
  if (/Mac pairing failed/i.test(text)) {
    return 'Mac pairing could not verify the setup. Confirm Remote Login is on, run both setup commands shown above, and pair again.';
  }
  return text;
}

function friendlyDeviceError(device, message) {
  if (device && isSamsungTV(device) && /no route to host|network is unreachable|connection refused|timed out|did not answer/i.test(String(message || ''))) {
    if (/no route to host|network is unreachable/i.test(String(message || ''))) {
      return 'The bridge cannot reach this TV right now. Confirm the TV is awake and both devices are on the same non-guest LAN, then scan again.';
    }
    if (/connection refused/i.test(String(message || ''))) {
      return 'The TV is visible on the network, but Samsung’s local remote service refused the connection. Enable mobile/device connection or network remote access, then pair again.';
    }
    if (device.discovery === 'arp') {
      return 'The TV is visible on the LAN, but Samsung’s local control service is not responding. Wake the TV, enable its mobile/device connection or network remote setting, confirm the bridge is not on a guest network, and scan again.';
    }
    return 'The bridge cannot open Samsung’s local control service. Wake the TV, enable its mobile/device connection or network remote setting, and scan again.';
  }
  return friendlyError(message);
}

function stateValue(value, fallback) {
  return value === undefined || value === null || value === '' || value === 'unknown' ? fallback : String(value);
}

function displayName(device) {
  return String(device?.alias || device?.name || device?.id || 'Unnamed device');
}

function kindLabel(device) {
  const theme = deviceTheme(device);
  return theme.machine;
}

function deviceTheme(device) {
  const category = String(device.category || '');
  if (category === 'tv_display') return {key: 'tv_display', label: 'TVs & displays', machine: device.platform || 'TV / display', role: device.kind === 'monitor' ? 'Display' : 'Television', glyph: 'TV'};
  if (category === 'console') return {key: 'consoles', label: 'Game consoles', machine: device.platform || 'Game console', role: 'Console', glyph: 'GC'};
  if (category === 'computer') {
    const platform = String(device.platform || '').toLowerCase();
    if (platform === 'macos') return {key: 'macos', label: 'Mac OS', machine: 'macOS', role: 'Mac computer', glyph: '⌘'};
    if (platform === 'windows laptop') return {key: 'windows', label: 'Windows', machine: 'Windows laptop', role: 'Laptop', glyph: 'PC'};
    if (platform === 'windows') return {key: 'windows', label: 'Windows', machine: 'Windows computer', role: 'Computer', glyph: 'PC'};
    if (platform === 'raspberry pi') return {key: 'raspberry_pi', label: 'Raspberry Pi', machine: 'Raspberry Pi', role: 'Single-board computer', glyph: 'RP'};
  }
  return {key: 'hidden', label: 'Unsupported inventory', machine: 'Unknown', role: 'Not shown', glyph: '?'};
}

function isSamsungTV(device) {
  return device.kind === 'tv' && String(device.manufacturer || '').toLowerCase() === 'samsung';
}

function samsungControlAvailable(device) {
  return isSamsungTV(device) && (Boolean(device.control_verified) || Boolean(device.paired));
}

function isRokuTV(device) {
  const text = `${device.manufacturer || ''} ${device.model || ''} ${device.name || ''}`.toLowerCase();
  return device.kind === 'tv' && (String(device.manufacturer || '').toLowerCase() === 'roku' || text.includes('roku'));
}

function isMacComputer(device) {
  const text = `${device.manufacturer || ''} ${device.model || ''} ${device.name || ''} ${device.discovery || ''}`.toLowerCase();
  return device.discovery !== 'arp' && device.kind === 'computer' && (String(device.manufacturer || '').toLowerCase() === 'apple' || /(macos|macbook|mac mini|mac-mini)/.test(text));
}

function isLocalMac(device) {
  return isMacComputer(device) && device.discovery === 'host';
}

function isConsole(device) {
  return String(device.category || '') === 'console' || String(device.kind || '') === 'console';
}

function requiresPairing(device) {
  return isSamsungTV(device) || (isMacComputer(device) && !isLocalMac(device));
}

function canControl(device) {
  return (samsungControlAvailable(device) || isRokuTV(device) || isMacComputer(device) || isConsole(device)) && !device.capabilities?.includes('unsupported');
}

function canOperate(device) {
  return samsungControlAvailable(device) || isRokuTV(device) || (isMacComputer(device) && !isLocalMac(device)) || (isConsole(device) && device.capabilities?.includes('wake_on_lan'));
}

function addTextRow(parent, label, value) {
  const term = document.createElement('dt');
  term.textContent = label;
  const description = document.createElement('dd');
  description.textContent = value;
  parent.append(term, description);
  return description;
}

function addButton(parent, label, onClick, className = '') {
  const button = document.createElement('button');
  button.textContent = label;
  button.className = className;
  button.addEventListener('click', onClick);
  parent.appendChild(button);
  return button;
}

function shellQuote(value) {
  return `'${String(value || '').replace(/'/g, "'\\''")}'`;
}

function macSetupCommands(device, username = '') {
  const target = `${username || 'YOUR_MAC_USERNAME'}@${device.ip || 'TARGET_MAC_IP'}`;
  const sshCommand = `test -f ~/.ssh/id_ed25519.pub || ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519 -N ''; cat ~/.ssh/id_ed25519.pub | ssh ${shellQuote(target)} 'umask 077; mkdir -p ~/.ssh; cat >> ~/.ssh/authorized_keys; chmod 600 ~/.ssh/authorized_keys'`;
  const ruleUser = username || 'YOUR_MAC_USERNAME';
  const sudoCommand = `printf '%s\\n' ${shellQuote(`${ruleUser} ALL=(root) NOPASSWD: /usr/bin/pmset -g ps, /usr/bin/pmset sleepnow`)} | sudo tee /etc/sudoers.d/local-device-bridge-pmset >/dev/null && sudo chmod 440 /etc/sudoers.d/local-device-bridge-pmset && sudo visudo -cf /etc/sudoers.d/local-device-bridge-pmset`;
  return {sshCommand, sudoCommand};
}

async function copyCommand(text, button) {
  try {
    await navigator.clipboard.writeText(text);
    const original = button.textContent;
    button.textContent = 'Copied';
    window.setTimeout(() => { button.textContent = original; }, 1400);
  } catch (_) {
    button.textContent = 'Copy failed';
  }
}

function commandBlock(parent, label, command) {
  const block = document.createElement('div');
  block.className = 'mac-command-block';
  const heading = document.createElement('div');
  heading.className = 'mac-command-heading';
  const title = document.createElement('strong');
  title.textContent = label;
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'copy-command';
  button.textContent = 'Copy command';
  const code = document.createElement('code');
  code.textContent = command;
  button.addEventListener('click', () => copyCommand(code.textContent, button));
  heading.append(title, button);
  block.append(heading, code);
  parent.appendChild(block);
  return code;
}

function route() {
  const parts = window.location.hash.replace(/^#\/?/, '').split('/').filter(Boolean);
  if (parts[0] === 'settings') return {id: null, settings: true};
  if (parts[0] === 'commands') return {id: null, commands: true};
  if (parts[0] === 'api') return {id: null, api: true};
  if (parts[0] !== 'device' || !parts[1]) return {id: null};
  let id = parts[1];
  try { id = decodeURIComponent(id); } catch (_) { return {id: null}; }
  return {id};
}

function navigate(id) {
  window.location.hash = `device/${encodeURIComponent(id)}`;
}

function navigateCommands() {
  window.location.hash = 'commands';
}

function navigateSettings() {
	if (route().settings) {
		loadSettings().catch(() => {});
		return;
	}
  window.location.hash = 'settings';
}

function navigateAPI() {
  window.location.hash = 'api';
}

function navigateHome() {
  window.location.hash = '';
}

async function pairDevice(id, username = '') {
  const device = deviceMap.get(String(id));
  if (device && isMacComputer(device) && !isLocalMac(device) && !username.trim()) {
    setPairingProgress('Enter the account name used to sign in to the Mac you want to control.', true);
    document.querySelector('#mac-username')?.focus();
    return;
  }
  setPairingProgress('Refreshing the device address…');
  try {
    // Refresh Samsung’s address immediately before pairing. TVs commonly
    // receive a new DHCP address while asleep, leaving an old persisted IP.
    if (device && isSamsungTV(device)) await scan(true);
    setPairingProgress(device && isSamsungTV(device)
      ? 'Waiting for the TV… Accept the request shown on the TV.'
      : 'Checking SSH access and the restricted Mac power permission…');
    const body = {};
    if (username) body.username = username;
    await api(`/api/v1/devices/${encodeURIComponent(id)}/pair`, {method: 'POST', body: JSON.stringify(body)});
    await refresh();
    // A pairing operation often happens while this device page is already
    // open. Setting the same hash does not emit hashchange, so force the
    // detail view to rebuild its pairing state and controls.
    lastRouteKey = '';
    renderRoute();
    navigate(id);
  } catch (error) {
    setPairingProgress(friendlyDeviceError(device, error.message), true);
  }
}

function setPairingProgress(message, error = false) {
  const feedback = document.querySelector('#pair-feedback');
  if (feedback) {
    feedback.hidden = !message;
    feedback.textContent = message;
    feedback.classList.toggle('feedback-error', error);
  }
  const actions = document.querySelector('#pairing-actions');
  if (actions) actions.querySelectorAll('button').forEach(button => { button.disabled = Boolean(message) && !error; });
}

async function unpairDevice(id) {
  const device = deviceMap.get(String(id));
  const label = device && isMacComputer(device) ? 'Remove this Mac pairing? Sleep controls will be locked until it is paired again.' : 'Remove this device pairing? You will need to pair it again before sending remote commands.';
  if (!window.confirm(label)) return;
  try {
    await api(`/api/v1/devices/${encodeURIComponent(id)}/unpair`, {method: 'POST'});
    await refresh();
    lastRouteKey = '';
    renderRoute();
    navigate(id);
  } catch (error) {
    const feedback = document.querySelector('#pair-feedback');
    if (feedback) {
      feedback.hidden = false;
      feedback.textContent = friendlyError(error.message);
      feedback.classList.add('feedback-error');
    }
  }
}

async function sendCommand(id, action, arguments_ = {}) {
  const device = deviceMap.get(String(id));
  if (!device || !canControl(device)) {
    setFeedback('This device does not have a control adapter yet.', true);
    return;
  }
  setFeedback(action === 'power_on' ? 'Sending Wake-on-LAN…' : 'Sending command…');
  try {
    const result = await api(`/api/v1/devices/${encodeURIComponent(id)}/commands`, {method: 'POST', body: JSON.stringify({action, arguments: arguments_})});
    lastCommandSucceeded.set(String(id), Date.now());
    setFeedback(result.message || 'Command completed.');
    if (route().id === String(id)) {
      // Refresh inventory metadata without rebuilding the remote DOM. A full
      // detail render here makes a phone remote jump when its feedback text
      // arrives and also interrupts a held volume button.
      await refreshInventoryOnly().catch(() => {});
      setFeedback(result.message || 'Command completed.');
    }
    if (route().commands) await loadCommandCenter();
  } catch (error) {
    setFeedback(friendlyError(error.message), true);
    if (/pairing expired|saved pairing was cleared|pair again|requested pairing approval again/i.test(error.message)) {
      await refresh().catch(() => {});
      if (route().id === String(id)) {
        lastRouteKey = '';
        renderRoute();
        setFeedback(friendlyError(error.message), true);
      }
    }
    if (route().commands) await loadCommandCenter();
  }
}

function homeCard(device, index) {
  const theme = deviceTheme(device);
  const card = document.createElement('article');
  card.className = `device-card theme-${theme.key} ${device.online ? 'is-online' : 'is-offline'}`;
  card.style.setProperty('--card-delay', `${Math.min(index * 35, 280)}ms`);
  card.innerHTML = '<div class="device-card-top"><div class="device-card-heading"><span class="device-icon"></span><div><span class="pill"></span><h3></h3><p class="device-role"></p></div></div><span class="device-status"><span class="device-status-dot" aria-hidden="true"></span><span class="device-status-label"></span></span></div><div class="device-specs"><div><span>Machine</span><strong class="machine-label"></strong></div><div><span>Address</span><strong class="device-address"></strong></div><div><span>MAC</span><strong class="device-mac"></strong></div><div><span>API</span><strong class="device-access"></strong></div></div><div class="device-card-footer"><span class="card-pairing-status"></span><button class="open-device">Open device <span aria-hidden="true">↗</span></button></div>';
  card.querySelector('.device-icon').textContent = theme.glyph;
  card.querySelector('.pill').textContent = theme.label;
  card.querySelector('h3').textContent = displayName(device);
  card.querySelector('.device-role').textContent = theme.role;
  card.querySelector('.device-status-label').textContent = device.online ? 'Online' : 'Offline';
  card.querySelector('.device-status').title = device.online ? 'Reachable on the local network' : 'Not currently reachable; remembered in the inventory';
  card.querySelector('.machine-label').textContent = theme.machine;
  card.querySelector('.device-address').textContent = device.ip || 'Address unavailable';
  card.querySelector('.device-mac').textContent = device.mac || 'Not reported';
  card.querySelector('.device-access').textContent = isConsole(device)
    ? (canOperate(device) ? 'Wake available' : 'Status only')
    : canControl(device) ? (requiresPairing(device) && !device.paired ? 'Pairing required' : 'Available') : 'Discovery only';
  const pairingStatus = isLocalMac(device) ? 'Bridge host' : (requiresPairing(device) ? (device.paired ? 'Paired' : 'Setup required') : '');
  card.querySelector('.card-pairing-status').textContent = pairingStatus;
  card.querySelector('.card-pairing-status').hidden = !pairingStatus;
  card.querySelector('.open-device').addEventListener('click', () => navigate(device.id));
  return card;
}

function sectionForTheme(theme, devicesInTheme, offset) {
  const section = document.createElement('section');
  section.className = `device-section theme-${theme.key} ${devicesInTheme.some(device => device.online) ? 'is-online' : 'is-offline'}`;
  const contentID = `device-section-content-${theme.key}`;
  section.innerHTML = `<div class="device-section-heading"><button class="section-toggle" type="button" aria-expanded="true" aria-controls="${contentID}"><span class="section-icon"></span><span class="section-label"><h3></h3><p></p></span><span class="section-count"></span><span class="section-chevron" aria-hidden="true">⌄</span></button></div><div id="${contentID}" class="device-section-content device-cards-grid"></div>`;
  section.querySelector('.section-icon').textContent = theme.glyph;
  section.querySelector('h3').textContent = theme.label;
  section.querySelector('p').textContent = theme.key === 'tv_display' ? 'Identified televisions and network displays' : theme.key === 'consoles' ? 'PlayStation, Xbox, and Nintendo devices on the LAN' : theme.key === 'macos' ? 'Apple computers discovered on the LAN' : theme.key === 'windows' ? 'Identified Windows computers on the LAN' : 'Single-board Linux computers with Raspberry Pi identity';
  const online = devicesInTheme.filter(device => device.online).length;
  section.querySelector('.section-count').textContent = `${online}/${devicesInTheme.length} online`;
  const sectionContent = section.querySelector('.device-cards-grid');
  const initiallyCollapsed = collapsedSections.has(theme.key);
  const toggle = section.querySelector('.section-toggle');
  section.classList.toggle('is-collapsed', initiallyCollapsed);
  toggle.setAttribute('aria-expanded', String(!initiallyCollapsed));
  sectionContent.hidden = initiallyCollapsed;
  sectionContent.classList.toggle('is-hidden', initiallyCollapsed);
  toggle.addEventListener('click', event => {
    event.preventDefault();
    const collapsed = section.classList.toggle('is-collapsed');
    if (collapsed) collapsedSections.add(theme.key);
    else collapsedSections.delete(theme.key);
    toggle.setAttribute('aria-expanded', String(!collapsed));
    sectionContent.hidden = collapsed;
    sectionContent.classList.toggle('is-hidden', collapsed);
  });
  devicesInTheme.forEach((device, index) => sectionContent.appendChild(homeCard(device, offset + index)));
  return section;
}

function renderHome() {
  const list = [...deviceMap.values()].filter(device => deviceTheme(device).key !== 'hidden');
  const online = list.filter(device => device.online).length;
  deviceCount.textContent = `${online}/${list.length} online`;
  devices.replaceChildren();
  if (list.length === 0) {
    const empty = document.createElement('div');
    empty.className = 'empty-state';
    empty.innerHTML = '<h3>No devices discovered yet</h3><p>Make sure the bridge host and devices are on the same network, then run a scan.</p>';
    devices.appendChild(empty);
    return;
  }
  const groups = new Map();
  list.forEach(device => {
    const theme = deviceTheme(device);
    if (!groups.has(theme.key)) groups.set(theme.key, {theme, devices: []});
    groups.get(theme.key).devices.push(device);
  });
  const order = ['tv_display', 'consoles', 'macos', 'windows', 'raspberry_pi'];
  let offset = 0;
  [...groups.values()].sort((left, right) => order.indexOf(left.theme.key) - order.indexOf(right.theme.key)).forEach(group => {
    devices.appendChild(sectionForTheme(group.theme, group.devices, offset));
    offset += group.devices.length;
  });
}

function remoteButton(parent, device, label, action, arguments_ = {}, title = '') {
  const button = addButton(parent, label, () => sendCommand(device.id, action, arguments_));
  if (action === 'power_on') button.classList.add('power-on');
  if (action === 'power_off') button.classList.add('power-off');
  if (title) button.title = title;
  return button;
}

function repeatRemoteButton(parent, device, label, action, arguments_ = {}) {
  const button = remoteButton(parent, device, label, action, arguments_, 'Tap once, or press and hold for repeated steps.');
  let delayTimer = null;
  let repeatTimer = null;
  const stopRepeating = () => {
    if (delayTimer) window.clearTimeout(delayTimer);
    if (repeatTimer) window.clearInterval(repeatTimer);
    delayTimer = null;
    repeatTimer = null;
  };
  button.addEventListener('pointerdown', () => {
    stopRepeating();
    delayTimer = window.setTimeout(() => {
      repeatTimer = window.setInterval(() => sendCommand(device.id, action, arguments_), 240);
    }, 420);
  });
  ['pointerup', 'pointercancel', 'pointerleave'].forEach(eventName => button.addEventListener(eventName, stopRepeating));
  return button;
}

function renderPairing(device) {
  const area = document.querySelector('#pairing-area');
  if (!area) return;
  const status = device.paired ? 'Paired' : 'Pairing required';
  if (isSamsungTV(device)) {
    if (!device.control_verified && !device.paired) {
      area.innerHTML = '<div class="pairing-block"><div class="pairing-heading"><div><p class="eyebrow">ACCESS RULES</p><h3>Samsung TV setup</h3></div><span class="pairing-status">Service not verified</span></div><ol class="setup-steps"><li><span>01</span><div>Wake the TV and keep it on the same trusted Wi‑Fi as this bridge.</div></li><li><span>02</span><div>On the TV open <b>Settings → General → Network → Expert Settings</b> and enable <b>Power On with Mobile</b>. Then open <b>Settings → General → External Device Manager → Device Connect Manager</b> and enable <b>Access Notification</b>.</div></li><li><span>03</span><div>Choose <b>Scan network</b> here. Pairing becomes available only after the bridge verifies Samsung’s local <b>/api/v2/</b> service; this page will not send a request before that check succeeds.</div></li></ol><p class="panel-intro">The TV was discovered, but its control service has not answered yet. This is an identified device, not a confirmed pairing target.</p></div>';
      return;
    }
    area.innerHTML = `<div class="pairing-block"><div class="pairing-heading"><div><p class="eyebrow">ACCESS RULES</p><h3>Samsung TV pairing</h3></div><span class="pairing-status ${device.paired ? 'is-paired' : ''}">${status}</span></div><ol class="setup-steps"><li><span>01</span><div>For wake: open <b>Settings → General → Network → Expert Settings</b> and enable <b>Power On with Mobile</b>.</div></li><li><span>02</span><div>For the remote: open <b>Settings → General → External Device Manager → Device Connect Manager</b>. Enable <b>Access Notification</b>; if this bridge is blocked in <b>Device List</b>, remove it. Some models place these items under <b>Connection → Network</b>.</div></li><li><span>03</span><div>Keep both devices on the same non-guest LAN. ${device.paired ? 'This TV is paired; leave network remote access enabled.' : 'Choose Pair TV once, accept the on-screen request, and wait for Paired before pressing remote buttons.'}</div></li></ol><div class="pairing-actions" id="pairing-actions"></div><div id="pairing-health" class="pairing-health" hidden></div><div id="pair-feedback" class="command-feedback" hidden></div></div>`;
    const actions = document.querySelector('#pairing-actions');
    if (device.paired) {
      addButton(actions, 'Unpair TV', () => unpairDevice(device.id), 'danger-button');
    } else {
      addButton(actions, 'Pair TV', () => pairDevice(device.id));
    }
    return;
  }
  if (isMacComputer(device)) {
    const local = isLocalMac(device);
    area.innerHTML = `<div class="pairing-block"><div class="pairing-heading"><div><p class="eyebrow">ACCESS RULES</p><h3>${local ? 'Bridge host access' : 'Mac setup guide'}</h3></div><span class="pairing-status ${local || device.paired ? 'is-paired' : ''}">${local ? 'Working here' : status}</span></div>${local ? '<p class="panel-intro mac-pairing-intro">This Mac is running local-device-bridge. It is available for status only, and its power buttons are intentionally hidden.</p>' : '<div class="mac-pairing-intro"><strong>What pairing enables</strong><p>The bridge can check this Mac and send only Wake and Sleep. It cannot open a general terminal, read your files, or run arbitrary commands.</p></div><ol class="mac-setup-guide"><li><span>1</span><div><strong>On the Mac you want to control</strong><p>Open <b>System Settings → General → Sharing</b>, turn on <b>Remote Login</b>, and allow the account you will enter below.</p></div></li><li><span>2</span><div><strong>Give the bridge a secure login</strong><p>On the bridge Mac mini, use the Copy button below, open Terminal, paste it, and press Return. It asks for the target Mac password once so future checks do not need a password.</p><div id="mac-ssh-command-block"></div></div></li><li><span>3</span><div><strong>Allow only Mac power actions</strong><p>On the Mac being controlled, use the second Copy button in Terminal. macOS will ask for your administrator password. This permission covers only power status and sleep.</p><div id="mac-sudo-command-block"></div></div></li><li><span>4</span><div><strong>Finish here</strong><p>Enter the short account name used to sign in to the target Mac, then choose Pair Mac. The bridge will test the setup and show the exact problem if anything is missing.</p></div></li></ol><div class="mac-username-row"><label for="mac-username">Target Mac account name</label><input id="mac-username" class="pairing-input" placeholder="for example: alex" autocomplete="username"></div>'}<div class="pairing-actions" id="pairing-actions"></div><div id="pair-feedback" class="command-feedback" hidden></div></div>`;
    const actions = document.querySelector('#pairing-actions');
    if (!local && !device.paired) {
      const input = document.querySelector('#mac-username');
      if (device.remote_user) input.value = device.remote_user;
      const pairButton = addButton(actions, 'Pair Mac', () => pairDevice(device.id, input.value.trim()));
      pairButton.disabled = !input.value.trim();
      input.addEventListener('input', () => { pairButton.disabled = !input.value.trim(); });
    } else if (!local) {
      addButton(actions, 'Unpair Mac', () => unpairDevice(device.id), 'danger-button');
    }
    if (!local) {
      const input = document.querySelector('#mac-username');
      if (device.remote_user) input.value = device.remote_user;
      const commands = macSetupCommands(device, input.value.trim());
      const sshCode = commandBlock(document.querySelector('#mac-ssh-command-block'), 'Run on the bridge Mac mini', commands.sshCommand);
      const sudoCode = commandBlock(document.querySelector('#mac-sudo-command-block'), 'Run on the target Mac', commands.sudoCommand);
      input.addEventListener('input', () => {
        const updated = macSetupCommands(device, input.value.trim());
        sshCode.textContent = updated.sshCommand;
        sudoCode.textContent = updated.sudoCommand;
      });
    }
    return;
  }
  if (isRokuTV(device)) {
    area.innerHTML = '<div class="pairing-block"><div class="pairing-heading"><div><p class="eyebrow">ACCESS RULES</p><h3>Roku TV access</h3></div><span class="pairing-status is-paired">Available</span></div><ol class="setup-steps"><li><span>01</span><div>On the Roku TV, open <b>Settings → System → Advanced system settings → Control by mobile apps</b>.</div></li><li><span>02</span><div>Choose <b>Enabled</b>, then keep the TV and bridge on the same non-guest network.</div></li><li><span>03</span><div>No bridge pairing prompt is required. The remote uses Roku’s local ECP service on port 8060.</div></li></ol><div class="command-feedback" hidden></div></div>';
    return;
  }
  const maker = String(device.manufacturer || '').toLowerCase();
  if (device.kind === 'tv' || device.kind === 'monitor') {
    let title = 'TV compatibility';
    let steps = 'This screen is discoverable, but its local remote protocol is not enabled in this build.';
    if (maker === 'lg') { title = 'LG webOS setup'; steps = 'Turn on <b>LG Connect Apps / Mobile TV On</b> in the TV network settings. LG uses a separate per-TV webOS pairing key; this build identifies the TV but keeps controls disabled until that adapter is installed and verified.'; }
    if (maker === 'sony') { title = 'Sony BRAVIA setup'; steps = 'Open <b>Settings → Network & Internet → Home network → IP control</b> (wording varies by model). Sony IP control is model-dependent; controls remain disabled unless the TV advertises a supported authenticated API.'; }
    area.innerHTML = `<div class="pairing-block"><div class="pairing-heading"><div><p class="eyebrow">COMPATIBILITY</p><h3>${title}</h3></div><span class="pairing-status">Discovery only</span></div><p class="panel-intro">${steps}</p><p class="panel-intro">Do not use Samsung pairing for this device. The bridge will enable a remote only when a compatible adapter is detected.</p></div>`;
    return;
  }
  if (device.kind === 'console') {
    const platform = device.platform || 'Game console';
    let steps = 'This console is visible, but it does not publish a safe universal LAN remote API.';
    if (/playstation/i.test(platform)) steps = 'On PS5: <b>Settings → System → Remote Play → Enable Remote Play</b>. For network wake from Rest Mode: <b>Settings → System → Power Saving → Features Available in Rest Mode</b>, then enable <b>Stay Connected to the Internet</b> and <b>Enable Turning On PS5 from Network</b>. Use Sony PS Remote Play for authenticated control.';
    if (/xbox/i.test(platform)) steps = 'Enable the console’s official Remote features and a network-connected sleep mode, then use the Xbox mobile app for authenticated control. The bridge will not imitate an Xbox account session.';
    if (/nintendo/i.test(platform)) steps = 'Nintendo does not provide a supported LAN remote/power API for this bridge. Discovery is informational only.';
    area.innerHTML = `<div class="pairing-block"><div class="pairing-heading"><div><p class="eyebrow">CONSOLE ACCESS</p><h3>${platform}</h3></div><span class="pairing-status is-paired">No pairing needed</span></div><p class="panel-intro">${steps}</p><p class="panel-intro">The bridge never requests a console account, password, or cloud token. Wake-on-LAN is available only when discovery reports a MAC address.</p></div>`;
    return;
  }
	if (device.kind === 'computer') {
	  const platform = device.platform || 'Computer';
	  let steps = 'This computer is identified on the LAN, but this release does not install a remote-control agent on it.';
	  if (/raspberry pi/i.test(platform)) steps = 'This Raspberry Pi is identified on the LAN. Linux has no universal safe pairing standard, so no SSH password or remote shell is requested. It remains inventory-only until a restricted helper is implemented and tested.';
	  else if (/windows/i.test(platform)) steps = 'This Windows computer is identified on the LAN. Remote Desktop and WinRM are not treated as universal pairing, so no credentials or power controls are requested in this release.';
	  area.innerHTML = `<div class="pairing-block"><div class="pairing-heading"><div><p class="eyebrow">COMPUTER ACCESS</p><h3>${platform}</h3></div><span class="pairing-status">Identified only</span></div><p class="panel-intro">${steps}</p></div>`;
	  return;
	}
  area.innerHTML = '<div class="pairing-block"><div class="pairing-heading"><div><p class="eyebrow">ACCESS RULES</p><h3>Pairing unavailable</h3></div><span class="pairing-status">Discovery only</span></div><p class="panel-intro">This device is visible on the network, but there is no safe control adapter or pairing method for it yet.</p></div>';
}

function renderRemote(device) {
  if (!canControl(device)) {
    const message = isSamsungTV(device)
      ? 'The TV is visible, but Samsung’s local control service has not been verified. Wake it, enable the network remote settings, and scan again; pairing will appear only after verification.'
      : 'This device is visible on the network, but local-device-bridge does not have a safe control adapter for it yet.';
    detailContent.querySelector('.remote-area').innerHTML = `<div class="unsupported-panel"><div><span class="support-mark">—</span><h3>No controls available</h3><p>${message}</p></div></div>`;
    return;
  }
  if (isSamsungTV(device) && !device.paired) {
    detailContent.querySelector('.remote-area').innerHTML = '<div class="unsupported-panel"><div><span class="support-mark">LOCK</span><h3>Controls locked</h3><p>Pair this TV and accept its on-screen prompt. If pairing fails, the Access Rules panel explains whether the TV setting or network connection needs attention.</p><div id="command-feedback" class="command-feedback remote-feedback" aria-live="polite"></div></div></div>';
    return;
  }
  if (isConsole(device)) {
    const remoteArea = detailContent.querySelector('.remote-area');
    remoteArea.innerHTML = '<h3>Console power</h3><p class="panel-intro">The bridge supports discovery and Wake-on-LAN only. Use the official console app for account-backed remote control.</p><div class="remote-power"></div><div id="command-feedback" class="command-feedback remote-feedback" aria-live="polite"></div>';
    const power = remoteArea.querySelector('.remote-power');
    const wake = remoteButton(power, device, 'Wake console', 'power_on', {}, 'Requires the console to allow network wake and discovery to know its MAC address.');
    if (!canOperate(device)) {
      wake.disabled = true;
      wake.title = 'No MAC address is known yet. Scan while the console is online, then try again.';
    }
    const off = remoteButton(power, device, 'Power off unavailable', 'power_off', {}, 'No safe universal local power-off API is available for consoles.');
    off.disabled = true;
    return;
  }
  if (isMacComputer(device)) {
    const remoteArea = detailContent.querySelector('.remote-area');
    if (isLocalMac(device)) {
      remoteArea.innerHTML = '<div class="unsupported-panel"><div><span class="support-mark">HOST</span><h3>Bridge host</h3><p>This Mac is running local-device-bridge. Power controls are intentionally hidden so the host cannot wake or sleep itself.</p></div></div>';
      return;
    }
    remoteArea.innerHTML = '<h3>Power remote</h3><p class="panel-intro">Mac control is intentionally limited to wake and sleep.</p><div class="remote-power"></div><div id="command-feedback" class="command-feedback remote-feedback" aria-live="polite"></div>';
    const power = remoteArea.querySelector('.remote-power');
    remoteButton(power, device, 'Wake Mac', 'power_on', {}, 'Requires Wake for network access on the Mac.');
    const sleep = remoteButton(power, device, 'Sleep Mac', 'power_off', {}, 'Requires local access or a paired Remote Login connection.');
    if (!isLocalMac(device) && !device.paired) {
      sleep.disabled = true;
      sleep.title = 'Pair this Mac first. Wake is available without pairing; sleep requires Remote Login and SSH pairing.';
    }
    return;
  }
  const remoteArea = detailContent.querySelector('.remote-area');
  remoteArea.innerHTML = '<h3>Remote</h3><p class="panel-intro">Use the remote below or your keyboard.</p><p class="keyboard-hint">Arrows · Enter · Escape · Page Up/Down volume · M mute</p><div class="remote-power"></div><div class="remote-nav"><button class="up">↑</button><button class="left">←</button><button class="ok">OK</button><button class="right">→</button><button class="down">↓</button></div><div class="remote-actions"></div><div id="command-feedback" class="command-feedback remote-feedback" aria-live="polite"></div><button class="command-history-link">Open Command Center ↗</button>';
  const power = remoteArea.querySelector('.remote-power');
  remoteButton(power, device, 'Wake TV', 'power_on', {}, isSamsungTV(device) ? 'Requires Samsung network standby / Power On with Mobile.' : 'Requires Wake-on-LAN support and a discovered MAC address.');
  remoteButton(power, device, 'Turn off', 'power_off', {}, isSamsungTV(device) ? 'Uses Samsung’s explicit power-off key.' : 'Uses the TV provider’s local power command.');
  const nav = remoteArea.querySelector('.remote-nav');
  [['up', 'UP'], ['left', 'LEFT'], ['ok', 'ENTER'], ['right', 'RIGHT'], ['down', 'DOWN']].forEach(([className, key]) => {
    nav.querySelector(`.${className}`).addEventListener('click', () => sendCommand(device.id, 'key', {key}));
  });
  const actions = remoteArea.querySelector('.remote-actions');
  repeatRemoteButton(actions, device, 'Vol +', 'volume_up');
  repeatRemoteButton(actions, device, 'Vol −', 'volume_down');
  remoteButton(actions, device, 'Mute', 'mute');
  remoteButton(actions, device, 'Home', 'key', {key: 'HOME'});
  remoteButton(actions, device, 'Back', 'key', {key: 'RETURN'});
  remoteButton(actions, device, 'Source', 'source', {source: isRokuTV(device) ? 'hdmi1' : 'source'});
  remoteButton(actions, device, 'Play / Pause', 'key', {key: 'PLAYPAUSE'}, isSamsungTV(device) ? 'Sends Samsung’s MediaPlayPause toggle key.' : 'Sends the TV provider’s play/pause key.');
  remoteArea.querySelector('.command-history-link').addEventListener('click', navigateCommands);
}

async function renameDevice(id, input, button) {
  const name = String(input.value || '').trim();
  if (!name) {
    setFeedback('Enter a friendly device name first.', true);
    return;
  }
  button.disabled = true;
  try {
    await api(`/api/v1/devices/${encodeURIComponent(id)}/name`, {method: 'POST', body: JSON.stringify({name})});
    await refresh();
    lastRouteKey = '';
    renderRoute();
    setFeedback(`Saved name: ${name}`);
  } catch (error) {
    setFeedback(friendlyError(error.message), true);
  } finally {
    button.disabled = false;
  }
}

function renderDevicePage(device) {
  const serial = ++renderSerial;
  const theme = deviceTheme(device);
  activeDeviceId = canOperate(device) ? device.id : null;
  document.querySelector('#detail-kind').textContent = theme.label;
  document.querySelector('#detail-title').textContent = displayName(device);
  document.querySelector('#detail-subtitle').textContent = `${theme.machine} · ${device.ip || 'address unavailable'}`;
  const initialState = isLocalMac(device) ? 'Bridge host' : (requiresPairing(device) ? (device.paired ? 'Paired' : 'Setup required') : (canControl(device) ? 'Ready' : ''));
  detailState.textContent = initialState;
  detailState.hidden = !initialState;
  detailState.className = `state-badge ${initialState === 'Paired' || initialState === 'Bridge host' ? 'is-ready' : ''}`;
  detailContent.innerHTML = '<div class="device-detail-grid"><article class="detail-panel status-area"><h3>At a glance</h3><p class="panel-intro">The few details that matter most for this device.</p><dl class="status-list" id="live-status"></dl><div class="status-note" id="status-note" hidden></div><div id="pairing-area"></div></article><article class="detail-panel remote-area"></article></div>';
  const live = document.querySelector('#live-status');
  addTextRow(live, 'Machine', theme.machine);
  if (device.model) addTextRow(live, 'Model', device.model);

  if (device.ip) addTextRow(live, 'Address', device.ip);
  addTextRow(live, 'Network', device.online ? 'Online' : 'Offline');
  const localAPIValue = addTextRow(live, 'Control API', canControl(device) ? 'Checking…' : 'Not available');
  if (requiresPairing(device)) addTextRow(live, 'Pairing', device.paired ? 'Paired' : 'Required');
  if (isLocalMac(device)) addTextRow(live, 'Access', 'This bridge host');
  const naming = document.createElement('div');
  naming.className = 'device-naming';
  naming.innerHTML = '<label for="device-friendly-name">Friendly name</label><div class="device-naming-row"><input id="device-friendly-name" maxlength="64" autocomplete="off"><button type="button">Save name</button></div><p>Use this name in the CLI, Telegram, or agent API instead of the device ID.</p>';
  naming.querySelector('input').value = device.alias || '';
  naming.querySelector('input').placeholder = device.name || device.id;
  naming.querySelector('button').addEventListener('click', () => renameDevice(device.id, naming.querySelector('input'), naming.querySelector('button')));
  live.parentElement.appendChild(naming);
  renderPairing(device);
  renderRemote(device);

  // A remembered Samsung record can be stale after DHCP changes or a TV
  // restart. Do one quiet repair scan when its detail page is opened, then
  // rebuild this page with the reconciled identity/address.
  if (isSamsungTV(device) && (!device.online || !device.paired) && !autoScanAttempts.has(String(device.id))) {
    autoScanAttempts.add(String(device.id));
    scan(true).then(() => {
      if (route().id === String(device.id)) {
        lastRouteKey = '';
        renderRoute();
      }
    }).catch(() => {});
  }

  const note = document.querySelector('#status-note');
  if (!canControl(device)) {
    note.hidden = false;
    note.textContent = 'This device is discovered and identified, but it does not have a safe control adapter yet.';
    return;
  }
  api(`/api/v1/devices/${encodeURIComponent(device.id)}/state`).then(state => {
    if (serial !== renderSerial) return;
    localAPIValue.textContent = 'Available';
    if (state.power && state.power !== 'unknown') addTextRow(live, 'Power', state.power);
    if (state.volume !== undefined) addTextRow(live, 'Volume', `${state.volume}%`);
    if (state.muted !== undefined) addTextRow(live, 'Muted', state.muted ? 'Yes' : 'No');
    if (state.source) addTextRow(live, 'Source', state.source);
    const pairingHealth = document.querySelector('#pairing-health');
    if (pairingHealth) pairingHealth.hidden = true;
    if (isSamsungTV(device) && (!state.power || state.power === 'unknown' || state.volume === undefined)) {
      note.hidden = false;
      note.textContent = 'Samsung confirms the local connection, but this model does not report panel power or absolute volume here.';
    }
  }).catch(error => {
    if (serial !== renderSerial) return;
    const remoteJustWorked = Date.now() - (lastCommandSucceeded.get(String(device.id)) || 0) < 15000;
    if (remoteJustWorked) {
      localAPIValue.textContent = 'Remote available';
      note.hidden = false;
      note.classList.remove('status-error');
      note.classList.add('status-soft-warning');
      note.textContent = 'The TV accepted the remote command, but this model did not return full status details. Controls remain available.';
      detailState.hidden = false;
      detailState.textContent = 'Remote ready';
      detailState.className = 'state-badge is-ready';
      return;
    }
    localAPIValue.textContent = 'Unavailable';
    note.hidden = false;
    note.classList.add('status-error');
    note.textContent = isMacComputer(device) ? `Mac control needs attention: ${friendlyError(error.message)}` : friendlyDeviceError(device, error.message);
    if (isSamsungTV(device) && device.paired) {
      const pairingHealth = document.querySelector('#pairing-health');
      if (pairingHealth) {
        pairingHealth.hidden = false;
        pairingHealth.classList.add('pairing-health-error');
        pairingHealth.textContent = /network remote setting|rejected the local API|remote access unavailable/i.test(error.message)
          ? 'Paired, but Samsung network remote access is disabled. Enable the TV setting, then pair the TV again.'
          : 'Paired, but the TV local API is unreachable. Turn the TV on, verify the current IP, and scan again.';
      }
      detailState.hidden = false;
      detailState.textContent = /network remote setting|rejected the local API|remote access unavailable/i.test(error.message) ? 'Network setting required' : 'TV unreachable';
      detailState.className = 'state-badge';
    }
  });
}

function commandLabel(action) {
  return String(action || '').replaceAll('_', ' ').replace(/\b\w/g, character => character.toUpperCase());
}

function renderCommandCenter() {
  const summary = document.querySelector('#command-summary');
  const search = commandSearch.trim().toLowerCase();
  const filtered = commandEvents.filter(event => {
    if (commandFilter === 'success' && !event.success) return false;
    if (commandFilter === 'failure' && event.success) return false;
    if (commandFilter === 'commands' && String(event.action || '').toLowerCase() === 'status') return false;
    if (!search) return true;
    const device = deviceMap.get(String(event.device_id));
    return `${event.action || ''} ${event.message || ''} ${event.source || ''} ${device?.name || ''}`.toLowerCase().includes(search);
  });
  if (summary) summary.textContent = `${filtered.length} of ${commandEvents.length} events shown`;
  commandList.replaceChildren();
  if (filtered.length === 0) {
    commandList.innerHTML = '<div class="command-empty"><span class="terminal-mark">_</span><h3>No matching events</h3><p>Try another filter or search term.</p></div>';
    return;
  }
  filtered.forEach(event => {
    const row = document.createElement('article');
    row.className = `command-row ${event.success ? 'command-success' : 'command-failure'}`;
    const device = deviceMap.get(String(event.device_id));
    row.innerHTML = '<span class="command-status" aria-hidden="true"></span><div class="command-main"><div class="command-title"></div><div class="command-message"></div></div><div class="command-meta"><span class="command-device"></span><span class="command-time"></span></div>';
    row.querySelector('.command-status').textContent = event.success ? '✓' : '△';
    row.querySelector('.command-title').textContent = commandLabel(event.action);
    row.querySelector('.command-message').textContent = event.message || (event.success ? 'Command completed' : 'Command failed');
    row.querySelector('.command-device').textContent = device ? displayName(device) : (event.device_id || 'Unknown device');
    row.querySelector('.command-time').textContent = `${formatDate(event.created_at)} · ${event.source || 'unknown source'}`;
    commandList.appendChild(row);
  });
}

async function loadCommandCenter() {
  commandList.innerHTML = '<div class="command-empty">Loading command history…</div>';
  try {
    const data = await api('/api/v1/events?limit=100');
    commandEvents = data.events || [];
    if (commandEvents.length === 0) {
      commandList.innerHTML = '<div class="command-empty"><span class="terminal-mark">_</span><h3>No commands yet</h3><p>Commands sent from the dashboard, CLI, or Telegram will appear here.</p></div>';
      return;
    }
    renderCommandCenter();
  } catch (error) {
    commandList.innerHTML = '<div class="command-empty"><h3>Command history unavailable</h3><p></p></div>';
    commandList.querySelector('p').textContent = friendlyError(error.message);
  }
}

async function loadSettings() {
  const feedback = document.querySelector('#settings-feedback');
  try {
    const data = await api('/api/v1/settings');
    const settings = data.settings || {};
    document.querySelector('#setting-displays').checked = Boolean(settings.show_display_devices);
    document.querySelector('#setting-consoles').checked = Boolean(settings.show_console_devices);
    document.querySelector('#setting-computers').checked = Boolean(settings.show_computer_devices);
    if (feedback) feedback.hidden = true;
  } catch (error) {
    if (!feedback) return;
    feedback.hidden = false;
    feedback.classList.add('feedback-error');
    feedback.textContent = friendlyError(error.message);
  }
}

async function saveSettings(event) {
  event.preventDefault();
  const feedback = document.querySelector('#settings-feedback');
  const settings = {
    show_display_devices: document.querySelector('#setting-displays').checked,
    show_console_devices: document.querySelector('#setting-consoles').checked,
    show_computer_devices: document.querySelector('#setting-computers').checked,
  };
  try {
    await api('/api/v1/settings', {method: 'POST', body: JSON.stringify(settings)});
    await refresh();
    navigateHome();
  } catch (error) {
    if (!feedback) return;
    feedback.hidden = false;
    feedback.classList.add('feedback-error');
    feedback.textContent = friendlyError(error.message);
  }
}

async function cancelSettings() {
  await loadSettings();
  navigateHome();
}

function apiText(parent, tag, text, className = '') {
  const element = document.createElement(tag);
  element.textContent = text;
  if (className) element.className = className;
  parent.appendChild(element);
  return element;
}

async function loadAgentAPI() {
  apiContent.innerHTML = '<div class="command-empty">Loading API details…</div>';
  try {
    const manifest = await api('/api/v1/agent/manifest');
    apiManifest = manifest;
    apiContent.replaceChildren();

    const overview = document.createElement('article');
    overview.className = 'api-panel api-overview';
    apiText(overview, 'h3', 'Connect an authorized agent');
    apiText(overview, 'p', 'This is the phone-friendly guide for the same local API. The QR link signs the phone browser in automatically; no API URL or chat command needs to be typed.');
    apiText(overview, 'p', 'For an external AI agent, run local-device-bridge agent token on the bridge computer and place that separate token in the agent’s private connector settings. Do not put it in a prompt, repository, Telegram chat, or QR code.');
    const code = document.createElement('code');
    code.className = 'api-command';
    code.textContent = 'local-device-bridge agent token';
    overview.appendChild(code);
    apiText(overview, 'p', 'Tell the agent: “Use local-device-bridge on my trusted LAN, authenticate with the provided bearer token, read the manifest and device guide first, ask before pairing or power commands, and use the device alias for later commands.”');
    const links = document.createElement('div');
    links.className = 'api-links';
    const openapi = document.createElement('a');
    openapi.href = '/api/v1/agent/openapi.json';
    openapi.target = '_blank';
    openapi.rel = 'noreferrer';
    openapi.textContent = 'Open generated OpenAPI JSON ↗';
    links.appendChild(openapi);
    overview.appendChild(links);
    apiContent.appendChild(overview);

    const workflow = document.createElement('article');
    workflow.className = 'api-panel api-workflow';
    apiText(workflow, 'h3', 'Pair, name, and control a device');
    apiText(workflow, 'p', 'An agent should follow this order. It prevents commands from being sent before the device has granted access.');
    const steps = [
      ['1', 'Discover', 'POST /api/v1/discovery/scan, then GET /api/v1/devices. Use the device id, alias, or original name returned by the inventory.'],
      ['2', 'Read the guide', 'GET /api/v1/devices/{reference}/guide. Follow the exact device steps. For Samsung, wake the TV, enable its network remote setting, then start pairing.'],
      ['3', 'Pair and accept', 'POST /api/v1/devices/{reference}/pair. Tell the user to accept the on-screen TV prompt, wait for paired=true, and only then send commands.'],
      ['4', 'Give it a friendly name', 'POST /api/v1/devices/{reference}/name with {"name":"Living Room TV"}. The alias is saved separately from the provider id.'],
      ['5', 'Control by name', 'Use the returned alias in later requests, for example POST /api/v1/devices/Living%20Room%20TV/commands with {"action":"power_on"}. Resolve ambiguity by using the id.'],
    ];
    const workflowList = document.createElement('ol');
    workflowList.className = 'api-workflow-list';
    steps.forEach(([number, title, description]) => {
      const item = document.createElement('li');
      item.innerHTML = '<span class="api-step-number"></span><div><strong class="api-step-title"></strong><p class="api-step-description"></p></div>';
      item.querySelector('.api-step-number').textContent = number;
      item.querySelector('.api-step-title').textContent = title;
      item.querySelector('.api-step-description').textContent = description;
      workflowList.appendChild(item);
    });
    workflow.appendChild(workflowList);
    apiContent.appendChild(workflow);

    const endpointsPanel = document.createElement('article');
    endpointsPanel.className = 'api-panel';
    apiText(endpointsPanel, 'h3', 'Available endpoints');
    apiText(endpointsPanel, 'p', 'These endpoints are implemented by this running bridge version.');
    const endpointList = document.createElement('div');
    endpointList.className = 'api-endpoint-list';
    (manifest.endpoints || []).forEach(endpoint => {
      const row = document.createElement('div');
      row.className = 'api-endpoint';
      apiText(row, 'span', endpoint.method || 'GET', 'api-method');
      apiText(row, 'code', endpoint.path || '', 'api-path');
      apiText(row, 'p', endpoint.description || '', 'api-description');
      endpointList.appendChild(row);
    });
    endpointsPanel.appendChild(endpointList);
    apiContent.appendChild(endpointsPanel);

    const actionsPanel = document.createElement('article');
    actionsPanel.className = 'api-panel';
    apiText(actionsPanel, 'h3', 'Normalized commands');
    apiText(actionsPanel, 'p', 'Use the action and arguments shown here with POST /devices/{device_id}/commands.');
    const actionList = document.createElement('div');
    actionList.className = 'api-action-list';
    (manifest.actions || []).forEach(action => {
      const row = document.createElement('div');
      row.className = 'api-action';
      apiText(row, 'code', action.action || '', 'api-action-name');
      apiText(row, 'span', action.description || '', 'api-description');
      if (action.arguments && Object.keys(action.arguments).length) apiText(row, 'code', JSON.stringify(action.arguments), 'api-arguments');
      actionList.appendChild(row);
    });
    actionsPanel.appendChild(actionList);
    apiContent.appendChild(actionsPanel);

    const rawPanel = document.createElement('details');
    rawPanel.className = 'api-panel api-raw';
    apiText(rawPanel, 'summary', 'Show raw agent manifest');
    const pre = document.createElement('pre');
    pre.textContent = JSON.stringify(manifest, null, 2);
    rawPanel.appendChild(pre);
    apiContent.appendChild(rawPanel);
  } catch (error) {
    apiContent.innerHTML = '<div class="command-empty"><h3>Agent API unavailable</h3><p></p></div>';
    apiContent.querySelector('p').textContent = friendlyError(error.message);
  }
}

function renderRoute() {
  const current = route();
  const device = current.id ? deviceMap.get(String(current.id)) : null;
  const hasDetail = Boolean(device);
  const hasCommands = Boolean(current.commands);
  const hasSettings = Boolean(current.settings);
  const hasAPI = Boolean(current.api);
  const deviceKey = device ? `${device.id}:${device.ip || ''}:${device.paired ? 'paired' : 'setup'}:${device.online ? 'online' : 'offline'}` : '';
  const routeKey = hasSettings ? 'settings' : (hasCommands ? 'commands' : (hasAPI ? 'api' : deviceKey));
  if (routeKey === lastRouteKey && hasDetail === !deviceView.hidden && hasCommands === !commandView.hidden && hasSettings === !settingsView.hidden && hasAPI === !apiView.hidden) return;
  lastRouteKey = routeKey;
  homeView.hidden = hasDetail || hasCommands || hasSettings || hasAPI;
  deviceView.hidden = !hasDetail;
  commandView.hidden = !hasCommands;
  settingsView.hidden = !hasSettings;
  apiView.hidden = !hasAPI;
  if (hasDetail) renderDevicePage(device);
  else if (hasCommands) {
    activeDeviceId = null;
    loadCommandCenter();
  } else if (hasSettings) {
    activeDeviceId = null;
    loadSettings();
  } else if (hasAPI) {
    activeDeviceId = null;
    loadAgentAPI();
  } else activeDeviceId = null;
}

async function refresh() {
  if (refreshInFlight) return;
  refreshInFlight = true;
  try {
    const data = await api('/api/v1/devices');
    deviceMap = new Map((data.devices || []).map(device => [String(device.id), device]));
    renderHome();
	if (route().settings) await loadSettings();
    renderRoute();
  } finally {
    refreshInFlight = false;
  }
}

async function refreshInventoryOnly() {
  const data = await api('/api/v1/devices');
  deviceMap = new Map((data.devices || []).map(device => [String(device.id), device]));
  renderHome();
}

async function scan(silent = false) {
  if (scanInFlight) return;
  scanInFlight = true;
  scanButton.disabled = true;
  scanButton.textContent = 'Scanning…';
  try {
    await api('/api/v1/discovery/scan', {method: 'POST'});
    await refresh();
  } catch (error) {
    if (!silent) setFeedback(friendlyError(error.message), true);
  } finally {
    scanInFlight = false;
    scanButton.disabled = false;
    scanButton.textContent = 'Scan network';
  }
}

async function pair() {
  try {
    await api('/api/v1/auth/session', {method: 'POST', body: JSON.stringify({token: document.querySelector('#token').value})});
    login.hidden = true;
    app.hidden = false;
    setAuthenticatedUI(true);
    await refresh();
  } catch (error) {
    document.querySelector('#login-error').textContent = error.message;
  }
}

document.querySelector('#login-form').addEventListener('submit', event => { event.preventDefault(); pair(); });
document.querySelector('#scan').addEventListener('click', () => scan(false));
document.querySelector('#back-to-devices').addEventListener('click', navigateHome);
document.querySelector('#command-center-button').addEventListener('click', navigateCommands);
document.querySelector('#api-button').addEventListener('click', navigateAPI);
document.querySelector('#settings-button').addEventListener('click', navigateSettings);
document.querySelector('#back-from-commands').addEventListener('click', navigateHome);
document.querySelector('#refresh-commands').addEventListener('click', loadCommandCenter);
document.querySelectorAll('[data-command-filter]').forEach(button => button.addEventListener('click', () => {
  commandFilter = button.dataset.commandFilter || 'all';
  document.querySelectorAll('[data-command-filter]').forEach(item => {
    const active = item === button;
    item.classList.toggle('is-active', active);
    item.setAttribute('aria-selected', active ? 'true' : 'false');
  });
  renderCommandCenter();
}));
document.querySelector('#command-search').addEventListener('input', event => {
  commandSearch = event.target.value;
  renderCommandCenter();
});
document.querySelector('#back-from-settings').addEventListener('click', navigateHome);
document.querySelector('#back-from-api').addEventListener('click', navigateHome);
document.querySelector('#cancel-settings').addEventListener('click', cancelSettings);
document.querySelector('#settings-form').addEventListener('submit', saveSettings);
window.addEventListener('hashchange', renderRoute);

document.addEventListener('keydown', event => {
  if (!activeDeviceId || !route().id || event.target.matches('input,textarea,select,button')) return;
  const keys = {ArrowUp: 'UP', ArrowDown: 'DOWN', ArrowLeft: 'LEFT', ArrowRight: 'RIGHT', Enter: 'ENTER', Escape: 'RETURN', Space: 'PLAYPAUSE', m: 'MUTE', M: 'MUTE'};
  const key = keys[event.key];
  if (!key && event.key !== 'PageUp' && event.key !== 'PageDown') return;
  event.preventDefault();
  if (event.key === 'PageUp') sendCommand(activeDeviceId, 'volume_up');
  else if (event.key === 'PageDown') sendCommand(activeDeviceId, 'volume_down');
  else if (key === 'MUTE') sendCommand(activeDeviceId, 'mute');
  else sendCommand(activeDeviceId, 'key', {key});
});

async function bootstrap() {
  try {
    await autoPairBrowser();
    await refresh();
    // The daemon starts its first scan in the background so the health
    // endpoint becomes available quickly. If the first inventory contains
    // only the bridge host, perform one quiet scan here instead of making a
    // new user click Scan network before any LAN devices can appear.
    if (!initialDiscoveryRequested && deviceMap.size <= 1) {
      initialDiscoveryRequested = true;
      await scan(true);
    }
    login.hidden = true;
    app.hidden = false;
    setAuthenticatedUI(true);
    renderRoute();
  } catch (_) {
    login.hidden = false;
    app.hidden = true;
    setAuthenticatedUI(false);
  }
}

checkBridgeContext();
bootstrap();
// The daemon performs the network scan. This lighter inventory poll keeps the
// dashboard current while the user is on the home page or inside a device,
// including when DHCP gives a TV or Mac a new address.
setInterval(() => { if (!app.hidden) refresh().catch(() => {}); }, 15000);
setInterval(() => { if (!app.hidden && route().commands) loadCommandCenter(); }, 10000);
