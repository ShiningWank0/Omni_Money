import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import {
    getAccounts,
    getTransactions,
    getCreditCardSettings,
    getItems
} from '../utils/api'

export const useAppStore = defineStore('app', () => {
    // 口座関連
    const accounts = ref([])
    const selectedFundItems = ref([])
    const creditCardItems = ref([])
    const itemNames = ref([])

    // 取引関連
    const transactions = ref([])
    const searchQuery = ref('')

    // UI状態
    const loading = ref(false)

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

        // 口座ごとの最新残高を取得して合算
        const latestBalances = {}
        const sorted = [...transactions.value].sort((a, b) => {
            const dateA = new Date(a.date)
            const dateB = new Date(b.date)
            if (dateA.getTime() !== dateB.getTime()) return dateA - dateB
            return a.id - b.id
        })

        for (const tx of sorted) {
            // クレジットカード項目は残高計算から除外
            if (creditCardItems.value.includes(tx.account || tx.fundItem)) {
                continue
            }
            latestBalances[tx.account || tx.fundItem] = tx.balance
        }

        return Object.values(latestBalances).reduce((sum, b) => sum + b, 0)
    })

    // 資金項目列を表示するかどうか
    const shouldShowFundItemColumn = computed(() => {
        return selectedFundItems.value.length !== 1
    })

    // 口座リストを取得
    async function fetchAccounts() {
        try {
            const result = await getAccounts()
            accounts.value = result || []
            // 初回は全選択
            if (selectedFundItems.value.length === 0 && accounts.value.length > 0) {
                selectedFundItems.value = [...accounts.value]
            }
        } catch (e) {
            console.error('口座リスト取得エラー:', e)
        }
    }

    // 取引履歴を取得
    async function fetchTransactions() {
        loading.value = true
        try {
            // 選択中の口座ごとに取引を取得して結合
            let allTransactions = []
            if (selectedFundItems.value.length === accounts.value.length) {
                // 全選択の場合はフィルタなしで取得
                allTransactions = await getTransactions('', searchQuery.value)
            } else {
                // 各口座ごとに取得
                for (const account of selectedFundItems.value) {
                    const txs = await getTransactions(account, searchQuery.value)
                    allTransactions = allTransactions.concat(txs || [])
                }
            }
            transactions.value = allTransactions || []
        } catch (e) {
            console.error('取引履歴取得エラー:', e)
        } finally {
            loading.value = false
        }
    }

    // クレジットカード設定を取得
    async function fetchCreditCardSettings() {
        try {
            const result = await getCreditCardSettings()
            creditCardItems.value = result || []
        } catch (e) {
            console.error('クレジットカード設定取得エラー:', e)
        }
    }

    // 項目名リストを取得
    async function fetchItems(account = '') {
        try {
            const result = await getItems(account)
            itemNames.value = result || []
        } catch (e) {
            console.error('項目リスト取得エラー:', e)
        }
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

    return {
        accounts,
        selectedFundItems,
        creditCardItems,
        itemNames,
        transactions,
        searchQuery,
        loading,
        actualFundItems,
        selectedFundItemDisplay,
        currentBalance,
        shouldShowFundItemColumn,
        fetchAccounts,
        fetchTransactions,
        fetchCreditCardSettings,
        fetchItems,
        toggleFundItem,
        toggleAllFundItems
    }
})
