// Layout scenarios intercept API traffic without mutating the disposable
// server. Keep writes explicit: adding a reply must not authorize every method.
export function panelApiReply(method, path, readReplies) {
  if (method === 'POST' && path === '/api/auth/keepalive') return { status: 204 }
  if (method === 'POST' && path === '/api/subscription-sync') {
    return jsonReply({ created: 0, has_more: false, errors: [] })
  }
  if (method === 'GET' && Object.hasOwn(readReplies, path)) return jsonReply(readReplies[path])
  return null
}

function jsonReply(value) {
  return { status: 200, contentType: 'application/json', body: JSON.stringify(value) }
}
