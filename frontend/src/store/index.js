import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import {
    getAccounts,
    getTransactions,
    getCreditCardSettings,
    getBankAccountSettings,
    getItems
} from '../utils/api.js'
import { exactInteger } from '../utils/exactAmount.js'

export const useAppStore = defineStore('app', () => {
    // 口座関連
    const accounts = ref([])
    const selectedFundItems = ref([])
    const creditCardItems = ref([])
    const bankAccountItems = ref([])
    const itemNames = ref([])

    // 取引関連
    const transactions = ref([])
    const searchQuery = ref('')

    // UI状態
    const loading = ref(false)
    let transactionRequestId = 0
    const readRequestIds = { accounts: 0, creditCards: 0, bankAccounts: 0, items: 0 }
    const loadErrors = ref({})
    const loadError = computed(() => Object.values(loadErrors.value).filter(Boolean).join(' '))

    async function readList(key, read, apply, label, throwOnError) {
        const requestId = ++readRequestIds[key]
        try {
            const result = await read()
            if (requestId !== readRequestIds[key]) return
            apply(result || [])
            loadErrors.value[key] = ''
        } catch (error) {
            if (requestId === readRequestIds[key]) {
                loadErrors.value[key] = `${label}を読み込めませんでした。`
            }
            if (throwOnError) throw error
        }
    }

    // 選択中の口座に含まれない口座を除いた実際の口座一覧
    const actualFundItems = computed(() => accounts.value)

    // 表示用の選択中口座テキスト
    const selectedFundItemDisplay = computed(() => {
        if (selectedFundItems.value.length === 0) return '口座を選択'
        if (selectedFundItems.value.length === accounts.value.length) return 'すべて'
        if (selectedFundItems.value.length === 1) return selectedFundItems.value[0]
        return `${selectedFundItems.value.length}件選択中`
    })

    // 現在の残高（選択中の口座の合算）
    const currentBalance = computed(() => {
        if (transactions.value.length === 0) return 0

        // 口座ごとの最新残高を1回の走査で取得して合算する。
        // 画面表示用ソートとは分離し、取引件数が増えても不要な全件sortを避ける。
        const latestByAccount = new Map()
        const creditCards = new Set(creditCardItems.value)

        for (const tx of transactions.value) {
            const account = tx.account || tx.fundItem
            if (creditCards.has(account)) continue

            const timestamp = Date.parse(tx.date) || 0
            const current = latestByAccount.get(account)
            if (!current || timestamp > current.timestamp ||
                (timestamp === current.timestamp && tx.id > current.id)) {
                latestByAccount.set(account, { balance: exactInteger(tx.balance, tx.balance_exact), timestamp, id: tx.id })
            }
        }

        return [...latestByAccount.values()].reduce((sum, entry) => sum + entry.balance, BigInt(0))
    })

    // 資金項目列を表示するかどうか
    const shouldShowFundItemColumn = computed(() => {
        return selectedFundItems.value.length !== 1
    })

    // 口座リストを取得
    async function fetchAccounts({ throwOnError = false } = {}) {
        await readList('accounts', getAccounts, result => {
            accounts.value = result
            if (selectedFundItems.value.length === 0 && accounts.value.length > 0) {
                selectedFundItems.value = [...accounts.value]
            }
        }, '口座一覧', throwOnError)
    }

    // 取引履歴を取得
    async function fetchTransactions({ throwOnError = false } = {}) {
        const requestId = ++transactionRequestId
        const selectedAccounts = [...selectedFundItems.value]
        const search = searchQuery.value
        loading.value = true
        try {
            let allTransactions = []
            if (selectedAccounts.length === accounts.value.length) {
                // 全選択の場合はフィルタなしで取得
                allTransactions = await getTransactions('', search)
            } else if (selectedAccounts.length > 0) {
                // 複数口座は並行取得し、直列待ちによる遅延を避ける。
                const results = await Promise.all(
                    selectedAccounts.map(account => getTransactions(account, search))
                )
                allTransactions = results.flatMap(result => result || [])
            }

            // 検索入力や口座選択の連打で、古いレスポンスが最新結果を上書きしないようにする。
            if (requestId === transactionRequestId) {
                transactions.value = allTransactions || []
                loadErrors.value.transactions = ''
            }
        } catch (e) {
            if (requestId === transactionRequestId) {
                loadErrors.value.transactions = '取引履歴を読み込めませんでした。'
            }
            if (throwOnError) throw e
        } finally {
            if (requestId === transactionRequestId) {
                loading.value = false
            }
        }
    }

    // クレジットカード設定を取得
    async function fetchCreditCardSettings({ throwOnError = false } = {}) {
        await readList('creditCards', () => getCreditCardSettings(), result => {
            creditCardItems.value = result
        }, 'クレジットカード設定', throwOnError)
    }

    // 銀行口座設定を取得
    async function fetchBankAccountSettings({ throwOnError = false } = {}) {
        await readList('bankAccounts', () => getBankAccountSettings(), result => {
            bankAccountItems.value = result
        }, '銀行口座設定', throwOnError)
    }

    // 項目名リストを取得
    async function fetchItems(account = '', { throwOnError = false } = {}) {
        await readList('items', () => getItems(account), result => {
            itemNames.value = result
        }, '項目一覧', throwOnError)
    }

    // 口座選択トグル
    function toggleFundItem(name) {
        const idx = selectedFundItems.value.indexOf(name)
        if (idx >= 0) {
            selectedFundItems.value.splice(idx, 1)
        } else {
            selectedFundItems.value.push(name)
        }
    }

    // 全選択/全解除
    function toggleAllFundItems() {
        if (selectedFundItems.value.length === accounts.value.length) {
            selectedFundItems.value = []
        } else {
            selectedFundItems.value = [...accounts.value]
        }
    }

    // 全状態をリセット（スナップショット復元後などに使用）
    function resetState() {
        transactionRequestId++
        for (const key of Object.keys(readRequestIds)) readRequestIds[key]++
        loadErrors.value = {}
        accounts.value = []
        selectedFundItems.value = []
        creditCardItems.value = []
        bankAccountItems.value = []
        itemNames.value = []
        transactions.value = []
        searchQuery.value = ''
        loading.value = false
    }

    return {
        accounts,
        selectedFundItems,
        creditCardItems,
        bankAccountItems,
        itemNames,
        transactions,
        searchQuery,
        loading,
        loadErrors,
        loadError,
        actualFundItems,
        selectedFundItemDisplay,
        currentBalance,
        shouldShowFundItemColumn,
        fetchAccounts,
        fetchTransactions,
        fetchCreditCardSettings,
        fetchBankAccountSettings,
        fetchItems,
        toggleFundItem,
        toggleAllFundItems,
        resetState
    }
})
