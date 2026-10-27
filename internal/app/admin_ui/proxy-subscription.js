const ProxySubscription = {
  timer: null, lastFinished: '', epoch: 0,
  text(zh,en) { return ProxyPoolUI.text(zh,en); },
  active(status) { return ['queued','waiting','validating','syncing'].includes(status.state); },
  error(code) {
    const messages = {
      invalid_url: ['请填写有效的 HTTPS 订阅地址', 'Enter a valid HTTPS subscription URL'],
      private_address: ['订阅地址必须是公网 HTTPS 地址', 'The subscription must use a public HTTPS address'],
      download_failed: ['无法下载订阅，请检查地址是否有效后重试', 'Cannot download the subscription. Check the URL and try again.'],
      invalid_subscription: ['该地址没有返回有效的 Clash 订阅，请检查订阅格式', 'The URL did not return a valid Clash subscription. Check the format.'],
      subscription_too_large: ['订阅文件过大，请减少节点后重试', 'The subscription is too large. Reduce its size and try again.'],
      sync_busy: ['同步任务繁忙，请稍后重试', 'Synchronization is busy. Try again shortly.'],
      restore_failed: ['重新导入已删除节点失败，请重试', 'Could not restore deleted nodes. Try again.'],
      interrupted: ['上次同步已中断，可以重新提交', 'The previous synchronization was interrupted. Submit again.'],
      queue_failed: ['保存失败，请重试', 'Could not save. Try again.'],
    };
    const pair=messages[code] || ['同步未完成，请重试', 'Synchronization did not complete. Try again.'];
    return this.text(...pair);
  },
  message(status) {
    if (!status.available) return this.text('订阅功能暂不可用，请稍后刷新', 'Subscription setup is unavailable. Refresh shortly.');
    switch (status.state) {
      case 'queued': return this.text('请求已保存，等待同步…', 'Request saved. Waiting to synchronize…');
      case 'waiting': return this.text('等待当前同步任务完成…', 'Waiting for the current synchronization to finish…');
      case 'validating': return this.text('正在下载并检查订阅…', 'Downloading and validating the subscription…');
      case 'syncing': return status.check_total > 0
        ? this.text(`正在检测代理 ${status.checked} / ${status.check_total}…`, `Checking proxies ${status.checked} / ${status.check_total}…`)
        : this.text('正在更新并检测代理…', 'Updating and checking proxies…');
      case 'succeeded': return this.text(`同步完成 · 订阅 ${status.node_count} 个节点，可用 ${status.imported} 个`, `Synchronized · ${status.node_count} subscription nodes, ${status.imported} available`);
      case 'warning': return status.code === 'deleted_nodes_skipped'
        ? this.text(`已同步，${status.excluded} 个曾删除节点已跳过；如需恢复，请勾选“重新导入”后再次同步`, `Synchronized; ${status.excluded} previously deleted nodes were skipped. Select reimport and synchronize again to restore them.`)
        : this.text('订阅已保存，暂未检测到可用代理，可稍后再次同步', 'Subscription saved. No available proxies detected yet; synchronize again later.');
      case 'failed': return this.error(status.code);
      default: return status.configured ? this.text('订阅已配置', 'Subscription configured') : this.text('尚未配置订阅', 'No subscription configured');
    }
  },
  async status() {
    const response=await fetch('api/proxy-subscription',{credentials:'include',cache:'no-store'});
    if (response.status===401) {renderLogin();throw new Error('unauthorized');}
    if (!response.ok) throw new Error('unavailable');
    return response.json();
  },
  async refreshSummary() {
    const summary=document.getElementById('proxySubscriptionSummary');
    if (!summary) return;
    try {
      const status=await this.status();
      if (!summary.isConnected) return;
      summary.textContent=this.message(status);
      if (status.job_id && ['succeeded','warning','failed'].includes(status.state) && status.job_id!==this.lastFinished) {
        this.lastFinished=status.job_id;
        if (currentPage==='proxies') await refreshPage();
      }
    } catch (_) {if(summary.isConnected)summary.textContent=this.text('订阅状态暂不可用', 'Subscription status unavailable');}
  },
  startSummary() {
    clearTimeout(this.timer);
    const epoch=++this.epoch;
    const poll=async()=>{
      if(currentPage!=='proxies' || epoch!==this.epoch)return;
      await this.refreshSummary();
      if(epoch===this.epoch)this.timer=setTimeout(poll,4000);
    };
    poll();
  },
  open() {
    const text=(zh,en)=>this.text(zh,en);
    let saving=false,ownedJob='',latest={available:false,state:'idle'};
    const render=(overlay,status)=>{
      latest=status;
      const active=this.active(status);
      overlay.querySelector('[data-subscription-state]').textContent=this.message(status);
      overlay.querySelector('[data-subscription-state]').dataset.state=status.state;
      overlay.querySelector('[data-subscription-host]').textContent=status.configured
        ? text(`当前订阅：${status.source_host || '已配置'}`, `Current subscription: ${status.source_host || 'configured'}`)
        : text('尚未配置订阅', 'No subscription configured');
      overlay.querySelector('[data-act="ok"]').disabled=saving || active || !status.available;
      overlay.querySelector('[name="subscription_url"]').disabled=saving || active;
      overlay.querySelector('[name="restore_deleted"]').disabled=saving || active;
      const progress=overlay.querySelector('progress');
      progress.hidden=!(active && status.check_total>0);
      if (!progress.hidden) {progress.max=status.check_total;progress.value=status.checked;}
      if (ownedJob===status.job_id && ['syncing','succeeded','warning'].includes(status.state)) overlay.querySelector('[name="subscription_url"]').value='';
    };
    const poll=async overlay=>{
      if (!overlay.isConnected)return;
      try {render(overlay,await this.status());}
      catch (_) {if(overlay.isConnected)overlay.querySelector('[data-subscription-state]').textContent=text('无法获取同步状态，请稍后重试', 'Cannot read synchronization status. Try again shortly.');}
      if (overlay.isConnected)setTimeout(()=>poll(overlay),2000);
    };
    return buildModal({
      title:text('订阅配置', 'Subscription setup'),
      desc:text('粘贴 Clash HTTPS 订阅地址，保存后自动更新并检测代理。沿用统一订阅，不改变各服务当前的出口设置。',
        'Paste a Clash HTTPS subscription URL to update and check proxies. Existing per-service egress settings are preserved.'),
      bodyHTML:`<p class="hint" data-subscription-host>${text('正在读取配置…','Loading configuration…')}</p>
        <div class="field"><label for="proxySubscriptionURL">${text('订阅地址','Subscription URL')}</label>
          <input id="proxySubscriptionURL" name="subscription_url" type="password" autocomplete="off" spellcheck="false" placeholder="https://…" style="width:100%">
          <div class="hint">${text('填写新地址可替换订阅；留空则刷新当前订阅。','Enter a new URL to replace the subscription, or leave blank to refresh it.')}</div>
        </div>
        <label class="proxy-subscription-restore"><input type="checkbox" name="restore_deleted"> ${text('重新导入之前删除的订阅节点','Reimport previously deleted subscription nodes')}</label>
        <div class="hint">${text('仅恢复新订阅中存在的节点；不勾选则保留之前的删除记录。','Only matching nodes in this subscription are restored. Leave unchecked to retain deletion preferences.')}</div>
        <div class="proxy-subscription-state" data-subscription-state role="status" aria-live="polite">${text('正在读取状态…','Loading status…')}</div>
        <progress hidden aria-label="${text('代理检测进度','Proxy check progress')}" style="width:100%"></progress>`,
      submitText:text('保存并同步','Save and synchronize'),
      onReady:overlay=>{
        overlay.querySelector('[data-act="cancel"]').textContent=text('关闭','Close');
        overlay.querySelector('[data-act="ok"]').disabled=true;
        poll(overlay);
      },
      onSubmit:async overlay=>{
        if (saving || this.active(latest)) return false;
        const input=overlay.querySelector('[name="subscription_url"]');
        const raw=input.value.trim();
        if (!raw && !latest.configured)return {error:text('请先填写订阅地址','Enter a subscription URL first')};
        if (raw) {
          try {const url=new URL(raw);if(url.protocol!=='https:' || url.username || url.password)throw new Error();}
          catch (_) {return {error:text('请填写有效的 HTTPS 订阅地址','Enter a valid HTTPS subscription URL')};}
        }
        saving=true;render(overlay,latest);
        const errorBox=overlay.querySelector('.modal-err');errorBox.style.display='none';errorBox.textContent='';
        try {
          const response=await fetch('api/proxy-subscription',{method:'PUT',credentials:'include',headers:{'Content-Type':'application/json','X-Gemini-Proxy':'1'},
            body:JSON.stringify({url:raw,restore_deleted:overlay.querySelector('[name="restore_deleted"]').checked})});
          const status=await response.json();
          if (response.status===409 && status.state) {latest=status;return false;}
          if (response.status===401) {renderLogin();return {error:text('登录已过期，请重新登录','Session expired. Log in again.')};}
          if (!response.ok)return {error:response.status===400?text('订阅地址无效，请检查后重试','Invalid subscription URL. Check it and try again.'):text('保存失败，请稍后重试','Could not save. Try again shortly.')};
          ownedJob=status.job_id;latest=status;
          this.lastFinished='';this.startSummary();
        } catch (_) {return {error:text('连接失败，请重试','Connection failed. Try again.')};}
        finally {saving=false;if(overlay.isConnected)render(overlay,latest);}
        return false; // Keep the dialog open to show progress and the result.
      },
    });
  },
};
