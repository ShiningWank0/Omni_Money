import { createPrivateKey } from 'node:crypto'
import { appendFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const repository = 'ShiningWank0/Omni_Money'
const repositoryID = 1171756271
const prefix = `/repos/${repository}`
const shaPattern = /^[a-f0-9]{40}$/

class CheckError extends Error {}

export function assertManualMainContext(env) {
  if (env.GITHUB_EVENT_NAME !== 'workflow_dispatch' || env.GITHUB_REPOSITORY !== repository ||
      env.GITHUB_REF !== 'refs/heads/main') {
    throw new CheckError('この設定確認はOmni_Moneyのmainからの手動実行専用です。')
  }
}

export function validateAppCredentials({ clientID, privateKey }) {
  if (typeof clientID !== 'string' || !clientID.trim()) {
    throw new CheckError('Repository variable NODE_SYNC_APP_CLIENT_IDを登録してください。')
  }
  if (typeof privateKey !== 'string' || !privateKey.trim()) {
    throw new CheckError('Repository secret NODE_SYNC_APP_PRIVATE_KEYを登録してください。')
  }
  let key
  try {
    // Match create-github-app-token's support for escaped newlines.
    key = createPrivateKey(privateKey.replace(/\\n/g, '\n').trim())
  } catch {
    throw new CheckError('RSA秘密鍵を読み込めません。元のPEM全体・BEGIN/END行・改行を確認してください。')
  }
  if (key.asymmetricKeyType !== 'rsa') {
    throw new CheckError('GitHub App用のRSA秘密鍵を登録してください。')
  }
}

function allowedPath(path) {
  return path === '/installation/repositories?per_page=100' ||
    path === `${prefix}/pulls?state=open&per_page=1` ||
    new RegExp(`^${prefix}/contents/\\.node-version\\?ref=[a-f0-9]{40}$`).test(path)
}

export function readOnlyGitHubRequest(token, fetchImpl = fetch) {
  if (typeof token !== 'string' || !token.trim()) throw new CheckError('Appトークンがありません。')
  return async path => {
    if (typeof path !== 'string' || !allowedPath(path)) throw new CheckError('設定確認で許可されていないAPIです。')
    let response
    try {
      response = await fetchImpl(`https://api.github.com${path}`, {
        method: 'GET',
        redirect: 'error',
        signal: AbortSignal.timeout(30_000),
        headers: {
          Authorization: `Bearer ${token}`,
          Accept: 'application/vnd.github+json',
          'X-GitHub-Api-Version': '2022-11-28',
        },
      })
    } catch {
      throw new CheckError('GitHub APIへの接続に失敗しました。')
    }
    if (!response.ok) {
      // Do not echo response bodies, credentials or third-party error text.
      throw new CheckError(`GitHub APIの読み取りに失敗しました（HTTP ${Number(response.status)}）。`)
    }
    try {
      return await response.json()
    } catch {
      throw new CheckError('GitHub APIから有効なJSON応答を取得できませんでした。')
    }
  }
}

export async function checkNodeSyncAppAccess({ ref, request }) {
  if (typeof ref !== 'string' || !shaPattern.test(ref)) throw new CheckError('確認対象のコミットが不正です。')
  const installation = await request('/installation/repositories?per_page=100')
  if (installation?.total_count !== 1 || !Array.isArray(installation.repositories) ||
      installation.repositories.length !== 1 || installation.repositories[0]?.id !== repositoryID ||
      installation.repositories[0]?.full_name !== repository) {
    throw new CheckError('発行トークンのアクセス先がOmni_Moneyだけに限定されていません。')
  }
  const file = await request(`${prefix}/contents/.node-version?ref=${ref}`)
  if (file?.type !== 'file' || file.encoding !== 'base64' || typeof file.content !== 'string' ||
      !Number.isSafeInteger(file.size) || file.size < 1 || file.size > 64) {
    throw new CheckError('.node-versionの読み取りを確認できませんでした。')
  }
  const nodeVersion = Buffer.from(file.content, 'base64').toString('utf8').trim()
  if (!/^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(nodeVersion)) {
    throw new CheckError('.node-versionの形式が不正です。')
  }
  const prs = await request(`${prefix}/pulls?state=open&per_page=1`)
  if (!Array.isArray(prs)) throw new CheckError('Pull requestsの読み取りを確認できませんでした。')
  return { nodeVersion }
}

function summary(text) {
  if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${text}\n`)
  console.log(text)
}

async function main() {
  assertManualMainContext(process.env)
  if (process.argv[2] === 'credentials') {
    validateAppCredentials({ clientID: process.env.NODE_SYNC_CLIENT_ID, privateKey: process.env.NODE_SYNC_PRIVATE_KEY })
    summary('Client IDの登録・RSA秘密鍵の読み込み：成功。')
  } else if (process.argv[2] === 'access') {
    const { nodeVersion } = await checkNodeSyncAppAccess({
      ref: process.env.NODE_SYNC_CHECK_SHA,
      request: readOnlyGitHubRequest(process.env.GH_TOKEN),
    })
    summary(`### Node同期Appの設定確認：成功
- Contents: write / Pull requests: readを指定したAppトークンの発行：成功
- 発行トークンの対象：Omni_Moneyのみ
- Contentsの読み取り：成功（Node ${nodeVersion}）
- Pull requestsの読み取り：成功
- コミット・PRへの書き込みは実行していません。同期コミットとCI再起動の確認は、次の対象Dependabot PRで行ってください。`)
  } else {
    throw new CheckError('credentialsまたはaccessを指定してください。')
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch(error => {
    summary(error instanceof CheckError ? error.message : '設定確認で予期しないエラーが発生しました。')
    process.exitCode = 1
  })
}
