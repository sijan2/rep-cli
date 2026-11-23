(async (job) => {
  const results = new Array(job.variants.length);
  const deadline = performance.now() + job.timeout_ms;
  let width = job.safe ? Math.min(4, job.variants.length) : 1;
  let stopped = false, attempted = 0, retries = 0, rateLimited = null;
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
        // Keep Chromium's native body consumer alive through load completion.
        // Draining/discarding a JS reader can race its loader's completion and
        // report ERR_ABORTED to CDP even after reader EOF. Native consumption
        // avoids that race without serializing the body through the RPC.
        result.bytes = (await response.arrayBuffer()).byteLength;
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
    for (const { result } of received) {
      // A rate limit, a refusal or any unsafe failure ends the job. Nothing is
      // retried around a 429: the CLI records the server's Retry-After as a
      // cooldown that every later request to this site honors.
      if (result.status === 429 || result.status === 403 || (!job.safe && !result.ok)) stopped = true;
      if (result.status === 429) rateLimited = { retry_ms: result.retry_ms };
    }
  }
  for (const variant of job.variants) {
    if (!results[variant.index]) results[variant.index] = {index: variant.index, status: 0, ok: false, bytes: 0, error: stopped ? (rateLimited ? 'not_sent_after_rate_limit' : 'not_sent_after_failure') : 'not_sent_deadline'};
  }
  return { variants: results, attempted, retries, stopped, rate_limited: rateLimited, complete: !pending.length && !stopped };
})(__REP_TRANSPORT_JOB__)
