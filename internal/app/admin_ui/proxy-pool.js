// Selection belongs to the visible filter. Never delete hidden rows implicitly.
const ProxyPoolUI = {
  items: [], selected: new Set(), query: '', status: '', busy: false,
  text(zh, en) { return typeof LANG !== 'undefined' && LANG === 'en' ? en : zh; },
  setItems(items) {
    this.items = items;
    const existing = new Set(items.map(p => p.id));
    this.selected = new Set([...this.selected].filter(id => existing.has(id)));
  },
  visible() {
    const query = this.query.trim().toLowerCase();
    return this.items.filter(p => (!query || `${p.id} ${p.name} ${p.url}`.toLowerCase().includes(query)) &&
      (!this.status || p.enabled === (this.status === 'enabled')));
  },
  rows() {
    return this.visible().map(p => `<tr>
      <td><input class="proxy-check" type="checkbox" data-proxy-id="${p.id}" aria-label="${this.text('选择代理', 'Select proxy')} ${p.id}" ${this.selected.has(p.id) ? 'checked' : ''} ${this.busy ? 'disabled' : ''} onchange="ProxyPoolUI.select(${p.id},this.checked)"></td>
      <td class="num nw">${p.id}</td>
      <td class="truncate" title="${escapeHtml(p.name)}">${escapeHtml(p.name)}</td>
      <td class="pre truncate" title="${escapeHtml(p.url)}">${escapeHtml(p.url)}</td>
      <td class="nw">${p.enabled ? '<span class="badge ok">已启用</span>' : '<span class="badge muted">已禁用</span>'}</td>
      <td class="num nw">${p.fail_count > 0 ? '<span class="badge fail">' + p.fail_count + '</span>' : p.fail_count}</td>
      <td class="pre truncate" title="${escapeHtml(p.last_error || '')}">${escapeHtml(p.last_error || '—')}</td>
      <td class="nw"><span class="actions">
        <button class="btn muted" data-proxy-action ${this.busy ? 'disabled' : ''} onclick="toggleProxy(${p.id})">${p.enabled ? '禁用' : '启用'}</button>
        <button class="btn muted" data-proxy-action ${this.busy ? 'disabled' : ''} onclick="resetProxy(${p.id})">重置</button>
        <button class="btn danger" data-proxy-action ${this.busy ? 'disabled' : ''} onclick="delProxy(${p.id})">删除</button>
      </span></td></tr>`).join('') || `<tr><td colspan="8" class="empty">${this.items.length ? this.text('没有符合条件的代理', 'No matching proxies') : this.text('还没有代理。点「+ 添加代理」加一个。', 'No proxies yet. Click "+ Add proxy" to add one.')}</td></tr>`;
  },
  filter() {
    if (this.busy) return;
    this.query = document.getElementById('proxySearch').value;
    this.status = document.getElementById('proxyStatus').value;
    this.selected.clear();
    document.getElementById('proxyRows').innerHTML = this.rows();
    this.updateSelection();
  },
  select(id, checked) {
    if (this.busy || !this.visible().some(p => p.id === id)) return;
    if (checked) this.selected.add(id); else this.selected.delete(id);
    this.updateSelection();
  },
  selectVisible(checked) {
    if (this.busy) return;
    this.selected.clear();
    if (checked) this.visible().forEach(p => this.selected.add(p.id));
    this.updateSelection();
  },
  selectDisabled() {
    if (this.busy) return;
    this.selected = new Set(this.visible().filter(p => !p.enabled).map(p => p.id));
    this.updateSelection();
  },
  updateSelection() {
    const all = document.getElementById('proxySelectAll');
    if (!all) return;
    const visible = this.visible();
    const selected = visible.filter(p => this.selected.has(p.id)).length;
    all.checked = visible.length > 0 && selected === visible.length;
    all.indeterminate = selected > 0 && selected < visible.length;
    all.disabled = this.busy || visible.length === 0;
    document.querySelectorAll('[data-proxy-id]').forEach(input => {
      input.checked = this.selected.has(Number(input.dataset.proxyId));
      input.disabled = this.busy;
    });
    document.querySelectorAll('[data-proxy-action], #proxySearch, #proxyStatus').forEach(el => { el.disabled = this.busy; });
    document.getElementById('proxySelectDisabled').disabled = this.busy || !visible.some(p => !p.enabled);
    document.getElementById('proxyDeleteSelected').disabled = this.busy || selected === 0;
    document.getElementById('proxySelectionCount').textContent = this.busy
      ? this.text(`正在处理 ${selected} 个代理…`, `Processing ${selected} proxies…`)
      : this.text(`已选 ${selected} 个 · 显示 ${visible.length} / ${this.items.length}`, `${selected} selected · Showing ${visible.length} / ${this.items.length}`);
  },
  async deleteSelected() {
    if (this.busy) return;
    const ids = this.visible().filter(p => this.selected.has(p.id)).map(p => p.id);
    if (!ids.length) return;
    if (ids.length > 1000) { flash(this.text('每次最多删除 1000 个代理，请缩小筛选范围', 'Delete up to 1000 proxies at a time. Narrow the filter.')); return; }
    this.busy = true;
    this.updateSelection();
    try {
      const confirmed = await modalConfirm({
        title: this.text('批量删除代理', 'Delete selected proxies'),
        desc: this.text(`确定删除选中的 ${ids.length} 个代理？Cookie 账号和请求历史会保留。订阅托管节点删除后不会被自动同步重新添加。`,
          `Delete the ${ids.length} selected proxies? Cookie accounts and request history will be kept. Deleted subscription nodes will not be recreated by automatic sync.`),
        confirmText: this.text(`删除 ${ids.length} 个代理`, `Delete ${ids.length} proxies`), danger: true,
      });
      if (!confirmed) return;
      const response = await fetch('api/proxies', {method: 'DELETE', credentials: 'include',
        headers: {'Content-Type': 'application/json'}, body: JSON.stringify({ids})});
      if (response.status === 401) { renderLogin(); throw new Error(this.text('登录已过期', 'Session expired')); }
      const result = await response.json();
      if (!response.ok || result.error) throw new Error(this.text('删除未完成，请刷新列表后重试', 'Deletion failed. Refresh the list and try again.'));
      this.selected.clear();
      flash(this.text(`已删除 ${result.deleted} 个代理`, `Deleted ${result.deleted} proxies`));
      if (currentPage === 'proxies') await refreshPage();
      await loadSideStat();
    } catch (error) {
      flash(error.message || this.text('删除失败，请重试', 'Deletion failed. Try again.'));
    } finally {
      this.busy = false;
      this.updateSelection();
    }
  },
};

function showProxySubscription() {
  return ProxySubscription.open();
}
