import { contextBridge, ipcRenderer, webUtils } from 'electron'

contextBridge.exposeInMainWorld('hermesDesktop', {
  getConnection: profile => ipcRenderer.invoke('xhermes:connection', profile),
  revalidateConnection: () => ipcRenderer.invoke('xhermes:connection:revalidate'),
  touchBackend: profile => ipcRenderer.invoke('xhermes:backend:touch', profile),
  getGatewayWsUrl: profile => ipcRenderer.invoke('xhermes:gateway:ws-url', profile),
  openSessionWindow: (sessionId, opts) => ipcRenderer.invoke('xhermes:window:openSession', sessionId, opts),
  openWindow: () => ipcRenderer.invoke('xhermes:window:openInstance'),
  claimAmbientCue: key => ipcRenderer.invoke('xhermes:ambient:claim', key),
  wakeIndicator: {
    getState: () => ipcRenderer.invoke('xhermes:wake-indicator:get'),
    setState: state => ipcRenderer.send('xhermes:wake-indicator:set', state),
    onState: callback => {
      const listener = (_event, state) => callback(state)
      ipcRenderer.on('xhermes:wake-indicator:state', listener)

      return () => ipcRenderer.removeListener('xhermes:wake-indicator:state', listener)
    }
  },
  petOverlay: {
    // Main renderer → main process: window lifecycle + drag. `request` is
    // `{ bounds, screen }`; resolves with the screen bounds it actually used.
    open: request => ipcRenderer.invoke('xhermes:pet-overlay:open', request),
    close: () => ipcRenderer.invoke('xhermes:pet-overlay:close'),
    setBounds: bounds => ipcRenderer.send('xhermes:pet-overlay:set-bounds', bounds),
    setIgnoreMouse: ignore => ipcRenderer.send('xhermes:pet-overlay:ignore-mouse', ignore),
    // Flip the overlay focusable (and focus it) while the composer needs keys.
    setFocusable: focusable => ipcRenderer.send('xhermes:pet-overlay:set-focusable', focusable),
    // Main renderer → overlay (forwarded by main): push the latest pet state.
    pushState: payload => ipcRenderer.send('xhermes:pet-overlay:state', payload),
    // Overlay → main renderer (forwarded by main): pop back in / composer submit.
    control: payload => ipcRenderer.send('xhermes:pet-overlay:control', payload),
    // Overlay subscribes to state pushes.
    onState: callback => {
      const listener = (_event, payload) => callback(payload)
      ipcRenderer.on('xhermes:pet-overlay:state', listener)

      return () => ipcRenderer.removeListener('xhermes:pet-overlay:state', listener)
    },
    // Main renderer subscribes to overlay control messages.
    onControl: callback => {
      const listener = (_event, payload) => callback(payload)
      ipcRenderer.on('xhermes:pet-overlay:control', listener)

      return () => ipcRenderer.removeListener('xhermes:pet-overlay:control', listener)
    }
  },
  // Quick Entry: the global-hotkey mini composer window. Main owns the OS
  // shortcut + the persisted preference; the quick window only captures text
  // and hands it back, and the primary renderer submits it through the normal
  // prompt path.
  quickEntry: {
    getSettings: () => ipcRenderer.invoke('xhermes:quick-entry:settings:get'),
    setSettings: patch => ipcRenderer.invoke('xhermes:quick-entry:settings:set', patch),
    submit: payload => ipcRenderer.send('xhermes:quick-entry:submit', payload),
    dismiss: () => ipcRenderer.send('xhermes:quick-entry:dismiss'),
    // Primary renderer → main → quick window: gateway connection state + the
    // recent-session options the target picker offers. Main caches the latest
    // payload so a freshly spawned quick window starts from truth.
    pushState: payload => ipcRenderer.send('xhermes:quick-entry:state', payload),
    // Quick window subscribes to those pushes.
    onState: callback => {
      const listener = (_event, payload) => callback(payload)
      ipcRenderer.on('xhermes:quick-entry:state', listener)

      return () => ipcRenderer.removeListener('xhermes:quick-entry:state', listener)
    },
    // Main → primary renderer: a submit captured by the quick window.
    onSubmit: callback => {
      const listener = (_event, payload) => callback(payload)
      ipcRenderer.on('xhermes:quick-entry:submit', listener)

      return () => ipcRenderer.removeListener('xhermes:quick-entry:submit', listener)
    },
    // Main → quick window: you were just summoned (reset draft + refocus).
    onShown: callback => {
      const listener = () => callback()
      ipcRenderer.on('xhermes:quick-entry:shown', listener)

      return () => ipcRenderer.removeListener('xhermes:quick-entry:shown', listener)
    }
  },
  getBootProgress: () => ipcRenderer.invoke('xhermes:boot-progress:get'),
  getConnectionConfig: profile => ipcRenderer.invoke('xhermes:connection-config:get', profile),
  saveConnectionConfig: payload => ipcRenderer.invoke('xhermes:connection-config:save', payload),
  applyConnectionConfig: payload => ipcRenderer.invoke('xhermes:connection-config:apply', payload),
  testConnectionConfig: payload => ipcRenderer.invoke('xhermes:connection-config:test', payload),
  sshConfigHosts: () => ipcRenderer.invoke('xhermes:ssh-config:hosts'),
  sshResolveHost: host => ipcRenderer.invoke('xhermes:ssh-config:resolve', host),
  probeConnectionConfig: remoteUrl => ipcRenderer.invoke('xhermes:connection-config:probe', remoteUrl),
  oauthLoginConnectionConfig: remoteUrl => ipcRenderer.invoke('xhermes:connection-config:oauth-login', remoteUrl),
  oauthLogoutConnectionConfig: remoteUrl => ipcRenderer.invoke('xhermes:connection-config:oauth-logout', remoteUrl),
  // XHermes Cloud: one portal login powers discovery + silent per-agent sign-in
  // (cloud-auto-discovery Phase 3).
  cloud: {
    status: () => ipcRenderer.invoke('xhermes:cloud:status'),
    login: () => ipcRenderer.invoke('xhermes:cloud:login'),
    logout: () => ipcRenderer.invoke('xhermes:cloud:logout'),
    discover: org => ipcRenderer.invoke('xhermes:cloud:discover', org),
    agentSignIn: dashboardUrl => ipcRenderer.invoke('xhermes:cloud:agent-sign-in', dashboardUrl)
  },
  profile: {
    get: () => ipcRenderer.invoke('xhermes:profile:get'),
    set: name => ipcRenderer.invoke('xhermes:profile:set', name)
  },
  api: request => ipcRenderer.invoke('xhermes:api', request),
  notify: payload => ipcRenderer.invoke('xhermes:notify', payload),
  requestMicrophoneAccess: () => ipcRenderer.invoke('xhermes:requestMicrophoneAccess'),
  readFileDataUrl: filePath => ipcRenderer.invoke('xhermes:readFileDataUrl', filePath),
  readFileDataUrlForAttach: filePath => ipcRenderer.invoke('xhermes:readFileDataUrlForAttach', filePath),
  dataUrlReadMax: {
    get: () => ipcRenderer.invoke('xhermes:data-url-read-max:get'),
    set: maxMb => ipcRenderer.invoke('xhermes:data-url-read-max:set', maxMb)
  },
  readFileText: filePath => ipcRenderer.invoke('xhermes:readFileText', filePath),
  selectPaths: options => ipcRenderer.invoke('xhermes:selectPaths', options),
  writeClipboard: text => ipcRenderer.invoke('xhermes:writeClipboard', text),
  readClipboard: () => ipcRenderer.invoke('xhermes:readClipboard'),
  saveImageFromUrl: url => ipcRenderer.invoke('xhermes:saveImageFromUrl', url),
  saveImageBuffer: (data, ext) => ipcRenderer.invoke('xhermes:saveImageBuffer', { data, ext }),
  saveClipboardImage: () => ipcRenderer.invoke('xhermes:saveClipboardImage'),
  getPathForFile: file => {
    try {
      return webUtils.getPathForFile(file) || ''
    } catch {
      return ''
    }
  },
  normalizePreviewTarget: (target, baseDir) => ipcRenderer.invoke('xhermes:normalizePreviewTarget', target, baseDir),
  watchPreviewFile: url => ipcRenderer.invoke('xhermes:watchPreviewFile', url),
  watchDirectory: dir => ipcRenderer.invoke('xhermes:watchDirectory', dir),
  stopPreviewFileWatch: id => ipcRenderer.invoke('xhermes:stopPreviewFileWatch', id),
  setActiveWork: payload => ipcRenderer.send('xhermes:active-work', payload),
  setTitleBarTheme: payload => ipcRenderer.send('xhermes:titlebar-theme', payload),
  setNativeTheme: mode => ipcRenderer.send('xhermes:native-theme', mode),
  setTranslucency: payload => ipcRenderer.send('xhermes:translucency', payload),
  setKeepAwake: on => ipcRenderer.send('xhermes:keep-awake', on),
  setPreviewShortcutActive: active => ipcRenderer.send('xhermes:previewShortcutActive', Boolean(active)),
  openExternal: url => ipcRenderer.invoke('xhermes:openExternal', url),
  openPreviewInBrowser: url => ipcRenderer.invoke('xhermes:openPreviewInBrowser', url),
  fetchLinkTitle: url => ipcRenderer.invoke('xhermes:fetchLinkTitle', url),
  sanitizeWorkspaceCwd: cwd => ipcRenderer.invoke('xhermes:workspace:sanitize', cwd),
  settings: {
    getDefaultProjectDir: () => ipcRenderer.invoke('xhermes:setting:defaultProjectDir:get'),
    setDefaultProjectDir: dir => ipcRenderer.invoke('xhermes:setting:defaultProjectDir:set', dir),
    pickDefaultProjectDir: () => ipcRenderer.invoke('xhermes:setting:defaultProjectDir:pick')
  },
  zoom: {
    // Current zoom of this window, as { level, percent }.
    get: () => ipcRenderer.invoke('xhermes:zoom:get'),
    setPercent: percent => ipcRenderer.send('xhermes:zoom:set-percent', percent),
    // Fires on every zoom change, including the Ctrl/Cmd +/-/0 shortcuts,
    // so the settings UI can stay in sync with the keyboard.
    onChanged: callback => {
      const listener = (_event, payload) => callback(payload)
      ipcRenderer.on('xhermes:zoom:changed', listener)

      return () => ipcRenderer.removeListener('xhermes:zoom:changed', listener)
    }
  },
  revealLogs: () => ipcRenderer.invoke('xhermes:logs:reveal'),
  getRecentLogs: () => ipcRenderer.invoke('xhermes:logs:recent'),
  readDir: dirPath => ipcRenderer.invoke('xhermes:fs:readDir', dirPath),
  gitRoot: startPath => ipcRenderer.invoke('xhermes:fs:gitRoot', startPath),
  revealPath: targetPath => ipcRenderer.invoke('xhermes:fs:reveal', targetPath),
  openDir: dirPath => ipcRenderer.invoke('xhermes:fs:openDir', dirPath),
  desktopPluginsRoot: () => ipcRenderer.invoke('xhermes:fs:desktopPluginsRoot'),
  renamePath: (targetPath, newName) => ipcRenderer.invoke('xhermes:fs:rename', targetPath, newName),
  writeTextFile: (filePath, content) => ipcRenderer.invoke('xhermes:fs:writeText', filePath, content),
  trashPath: targetPath => ipcRenderer.invoke('xhermes:fs:trash', targetPath),
  git: {
    worktreeList: repoPath => ipcRenderer.invoke('xhermes:git:worktreeList', repoPath),
    worktreeAdd: (repoPath, options) => ipcRenderer.invoke('xhermes:git:worktreeAdd', repoPath, options),
    worktreeRemove: (repoPath, worktreePath, options) =>
      ipcRenderer.invoke('xhermes:git:worktreeRemove', repoPath, worktreePath, options),
    branchSwitch: (repoPath, branch) => ipcRenderer.invoke('xhermes:git:branchSwitch', repoPath, branch),
    branchList: repoPath => ipcRenderer.invoke('xhermes:git:branchList', repoPath),
    baseBranchList: repoPath => ipcRenderer.invoke('xhermes:git:baseBranchList', repoPath),
    repoStatus: repoPath => ipcRenderer.invoke('xhermes:git:repoStatus', repoPath),
    fileDiff: (repoPath, filePath) => ipcRenderer.invoke('xhermes:git:fileDiff', repoPath, filePath),
    scanRepos: (roots, options) => ipcRenderer.invoke('xhermes:git:scanRepos', roots, options),
    review: {
      list: (repoPath, scope, baseRef) => ipcRenderer.invoke('xhermes:git:review:list', repoPath, scope, baseRef),
      diff: (repoPath, filePath, scope, baseRef, staged) =>
        ipcRenderer.invoke('xhermes:git:review:diff', repoPath, filePath, scope, baseRef, staged),
      stage: (repoPath, filePath) => ipcRenderer.invoke('xhermes:git:review:stage', repoPath, filePath),
      unstage: (repoPath, filePath) => ipcRenderer.invoke('xhermes:git:review:unstage', repoPath, filePath),
      revert: (repoPath, filePath) => ipcRenderer.invoke('xhermes:git:review:revert', repoPath, filePath),
      revParse: (repoPath, ref) => ipcRenderer.invoke('xhermes:git:review:revParse', repoPath, ref),
      commit: (repoPath, message, push) => ipcRenderer.invoke('xhermes:git:review:commit', repoPath, message, push),
      commitContext: repoPath => ipcRenderer.invoke('xhermes:git:review:commitContext', repoPath),
      push: repoPath => ipcRenderer.invoke('xhermes:git:review:push', repoPath),
      shipInfo: repoPath => ipcRenderer.invoke('xhermes:git:review:shipInfo', repoPath),
      createPr: repoPath => ipcRenderer.invoke('xhermes:git:review:createPr', repoPath)
    }
  },
  terminal: {
    cwd: id => ipcRenderer.invoke('xhermes:terminal:cwd', id),
    dispose: id => ipcRenderer.invoke('xhermes:terminal:dispose', id),
    resize: (id, size) => ipcRenderer.invoke('xhermes:terminal:resize', id, size),
    start: options => ipcRenderer.invoke('xhermes:terminal:start', options),
    write: (id, data) => ipcRenderer.invoke('xhermes:terminal:write', id, data),
    onData: (id, callback) => {
      const channel = `xhermes:terminal:${id}:data`
      const listener = (_event, payload) => callback(payload)
      ipcRenderer.on(channel, listener)

      return () => ipcRenderer.removeListener(channel, listener)
    },
    onExit: (id, callback) => {
      const channel = `xhermes:terminal:${id}:exit`
      const listener = (_event, payload) => callback(payload)
      ipcRenderer.on(channel, listener)

      return () => ipcRenderer.removeListener(channel, listener)
    }
  },
  onClosePreviewRequested: callback => {
    const listener = () => callback()
    ipcRenderer.on('xhermes:close-preview-requested', listener)

    return () => ipcRenderer.removeListener('xhermes:close-preview-requested', listener)
  },
  onOpenFolderRequested: callback => {
    const listener = () => callback()
    ipcRenderer.on('xhermes:open-folder-requested', listener)

    return () => ipcRenderer.removeListener('xhermes:open-folder-requested', listener)
  },
  onOpenUpdatesRequested: callback => {
    const listener = () => callback()
    ipcRenderer.on('xhermes:open-updates', listener)

    return () => ipcRenderer.removeListener('xhermes:open-updates', listener)
  },
  onDeepLink: callback => {
    const listener = (_event, payload) => callback(payload)
    ipcRenderer.on('xhermes:deep-link', listener)

    return () => ipcRenderer.removeListener('xhermes:deep-link', listener)
  },
  signalDeepLinkReady: () => ipcRenderer.invoke('xhermes:deep-link-ready'),
  onWindowStateChanged: callback => {
    const listener = (_event, payload) => callback(payload)
    ipcRenderer.on('xhermes:window-state-changed', listener)

    return () => ipcRenderer.removeListener('xhermes:window-state-changed', listener)
  },
  onFocusSession: callback => {
    const listener = (_event, sessionId) => callback(sessionId)
    ipcRenderer.on('xhermes:focus-session', listener)

    return () => ipcRenderer.removeListener('xhermes:focus-session', listener)
  },
  onNotificationAction: callback => {
    const listener = (_event, payload) => callback(payload)
    ipcRenderer.on('xhermes:notification-action', listener)

    return () => ipcRenderer.removeListener('xhermes:notification-action', listener)
  },
  onPreviewFileChanged: callback => {
    const listener = (_event, payload) => callback(payload)
    ipcRenderer.on('xhermes:preview-file-changed', listener)

    return () => ipcRenderer.removeListener('xhermes:preview-file-changed', listener)
  },
  onBackendExit: callback => {
    const listener = (_event, payload) => callback(payload)
    ipcRenderer.on('xhermes:backend-exit', listener)

    return () => ipcRenderer.removeListener('xhermes:backend-exit', listener)
  },
  // Soft gateway-mode apply finished tearing down the primary backend. Renderer
  // should wipe session lists + re-dial without a window reload.
  onConnectionApplied: callback => {
    const listener = () => callback()
    ipcRenderer.on('xhermes:connection:applied', listener)

    return () => ipcRenderer.removeListener('xhermes:connection:applied', listener)
  },
  onPowerResume: callback => {
    const listener = () => callback()
    ipcRenderer.on('xhermes:power-resume', listener)

    return () => ipcRenderer.removeListener('xhermes:power-resume', listener)
  },
  // AC ↔ battery transitions; renderers slow their backstop polls on battery.
  getOnBattery: () => ipcRenderer.invoke('xhermes:power-battery:get'),
  onBatteryChanged: callback => {
    const listener = (_event, onBattery) => callback(Boolean(onBattery))
    ipcRenderer.on('xhermes:power-battery', listener)

    return () => ipcRenderer.removeListener('xhermes:power-battery', listener)
  },
  onBootProgress: callback => {
    const listener = (_event, payload) => callback(payload)
    ipcRenderer.on('xhermes:boot-progress', listener)

    return () => ipcRenderer.removeListener('xhermes:boot-progress', listener)
  },
  // First-launch bootstrap progress -- emitted by the install.ps1 stage
  // runner in main.ts (apps/desktop/electron/bootstrap-runner.ts).
  // Renderer's install overlay subscribes to live events and queries the
  // current snapshot via getBootstrapState() to recover after a devtools
  // reload mid-bootstrap.
  getBootstrapState: () => ipcRenderer.invoke('xhermes:bootstrap:get'),
  continueBootstrapLocal: () => ipcRenderer.invoke('xhermes:bootstrap:continue-local'),
  resetBootstrap: () => ipcRenderer.invoke('xhermes:bootstrap:reset'),
  repairBootstrap: () => ipcRenderer.invoke('xhermes:bootstrap:repair'),
  cancelBootstrap: () => ipcRenderer.invoke('xhermes:bootstrap:cancel'),
  onBootstrapEvent: callback => {
    const listener = (_event, payload) => callback(payload)
    ipcRenderer.on('xhermes:bootstrap:event', listener)

    return () => ipcRenderer.removeListener('xhermes:bootstrap:event', listener)
  },
  getVersion: () => ipcRenderer.invoke('xhermes:version'),
  getRemoteDisplayReason: () => ipcRenderer.invoke('xhermes:get-remote-display-reason'),
  uninstall: {
    summary: () => ipcRenderer.invoke('xhermes:uninstall:summary'),
    run: mode => ipcRenderer.invoke('xhermes:uninstall:run', { mode })
  },
  updates: {
    check: () => ipcRenderer.invoke('xhermes:updates:check'),
    apply: opts => ipcRenderer.invoke('xhermes:updates:apply', opts),
    getBranch: () => ipcRenderer.invoke('xhermes:updates:branch:get'),
    setBranch: name => ipcRenderer.invoke('xhermes:updates:branch:set', name),
    onProgress: callback => {
      const listener = (_event, payload) => callback(payload)
      ipcRenderer.on('xhermes:updates:progress', listener)

      return () => ipcRenderer.removeListener('xhermes:updates:progress', listener)
    }
  },
  themes: {
    fetchMarketplace: id => ipcRenderer.invoke('xhermes:vscode-theme:fetch', id),
    searchMarketplace: query => ipcRenderer.invoke('xhermes:vscode-theme:search', query)
  },
  // Find-in-page (Ctrl/Cmd+F): delegates to Electron's
  // webContents.findInPage on the IPC sender's window so a Cmd+F pressed
  // in a secondary session window searches THAT window, not the primary.
  // `onFoundInPage` returns the unsubscribe fn; the renderer wires it via
  // `initFindInPageListener` in store/find-in-page.ts and tears it down
  // when the FindBar unmounts.
  findInPage: (query, options) => ipcRenderer.invoke('xhermes:find-in-page', query, options),
  stopFindInPage: () => ipcRenderer.invoke('xhermes:stop-find-in-page'),
  onFoundInPage: callback => {
    const listener = (_event, result) => callback(result)
    ipcRenderer.on('xhermes:found-in-page', listener)

    return () => ipcRenderer.removeListener('xhermes:found-in-page', listener)
  }
})
