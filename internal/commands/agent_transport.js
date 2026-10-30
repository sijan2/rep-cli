(async (job) => {
  const results = new Array(job.variants.length);
  const deadline = performance.now() + job.timeout_ms;
  let width = job.safe ? Math.min(4, job.variants.length) : 1;
  let stopped = false, attempted = 0, retries = 0;
  const run = async (variant) => {
    const result = { index: variant.index, status: 0, ok: false, bytes: 0 };
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), Math.max(1, deadline - performance.now()));
    try {
      const init = { method: variant.method, headers: variant.headers || {},
        credentials: 'include', cache: 'no-store', redirect: job.safe ? 'follow' : 'error', signal: controller.signal };
      if (variant.body != null) init.body = variant.body;
      const response = await fetch(variant.url, init);
      result.status = response.status;
      result.ok = response.ok;
      const retryAfter = response.headers.get('retry-after') || '';
      result.retry_ms = Math.min(5000, Math.max(250, Number(retryAfter) * 1000 || Date.parse(retryAfter) - Date.now() || 250));
      if (response.body) {
        const reader = response.body.getReader();
        try {
          for (;;) {
            const chunk = await reader.read();
            if (chunk.done) break;
            result.bytes += chunk.value.byteLength;
          }
        } finally { reader.releaseLock(); }
      }
    } catch (_) {
      result.error = controller.signal.aborted ? 'request_timeout' : 'request_failed';
      result.ok = false;
    } finally { clearTimeout(timer); }
    return result;
  };
  let pending = job.variants.map((variant) => ({ variant, tries: 0 }));
  while (pending.length && !stopped && performance.now() < deadline) {
    const batch = pending.splice(0, width);
    const received = await Promise.all(batch.map(async (entry) => {
      attempted++;
      const result = await run(entry.variant);
      results[entry.variant.index] = result;
      return { entry, result };
    }));
    let backoff = 0;
    for (const { entry, result } of received) {
      if (result.status === 403 || (!job.safe && !result.ok)) stopped = true;
      if (job.safe && result.status === 429) {
        width = Math.max(1, Math.floor(width / 2));
        backoff = Math.max(backoff, result.retry_ms * (entry.tries + 1));
        if (entry.tries < 2) { pending.push({ variant: entry.variant, tries: entry.tries + 1 }); retries++; }
      }
    }
    if (backoff && !stopped) await new Promise(resolve => setTimeout(resolve, Math.min(backoff, Math.max(0, deadline - performance.now()))));
  }
  for (const variant of job.variants) {
    if (!results[variant.index]) results[variant.index] = {index: variant.index, status: 0, ok: false, bytes: 0, error: stopped ? 'not_sent_after_failure' : 'not_sent_deadline'};
  }
  return { variants: results, attempted, retries, stopped, complete: !pending.length && !stopped };
})(__REP_TRANSPORT_JOB__)
