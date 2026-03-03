// Vue.js アプリケーションのロジック

const { createApp } = Vue;

createApp({
    data() {
        return {
            transactions: [],
            fundItemNames: [],
            itemNames: // 項目名リスト
            [],
            selectedFundItems: [], // 統一された資金項目選択（全画面で共有）
            dateSortOrder: 'desc',
            showAccountDropdown: false,
            loading: true,
            searchQuery: '',
            searchTimeout: null,
            showAddTransactionModal: false,
            showFundItemDropdown: false, // 資金項目ドロップダウンの表示状態
            isEditMode: false, // 追加: 編集モードかどうか
            editTransactionId: null, // 追加: 編集中の取引ID
            newTransaction: {
                fundItem: '',
                date: '',
                time: '',
                item: '',
                type: 'expense',
                amount: ''
            },
            showMenu: false, // ハンバーガーメニューの表示状態
            showGraph: false, // グラフモーダルの表示状態
            showRatioModal: false, // 収支比率モーダル表示状態
            showItemizedModal: false, // 項目別収支モーダル表示状態
            showGraphFundItemDropdown: false, // グラフ用資金項目ドロップダウン表示状態
            showRatioFundItemDropdown: false, // 収支比率用資金項目ドロップダウン表示状態
            showItemizedFundItemDropdown: false, // 項目別収支用資金項目ドロップダウン表示状態
            ratioChartInstance: null, // 収支比率チャートインスタンス
            incomeItemChartInstance: null, // 収入項目別チャートインスタンス
            expenseItemChartInstance: null, // 支出項目別チャートインスタンス
            graphDisplayUnit: 'day', // グラフ表示単位: 'day','month','year'
            ratioDisplayUnit: 'all', // 収支比率グラフ表示単位: 'all','day','month','year'
            itemizedDisplayUnit: 'all', // 項目別収支グラフ表示単位: 'all','day','month','year'
            ratioCurrentDate: new Date(), // 収支比率グラフの現在選択日付
            itemizedCurrentDate: new Date(), // 項目別収支グラフの現在選択日付
            ratioAvailablePeriods: [], // 収支比率グラフで利用可能な期間リスト
            itemizedAvailablePeriods: [], // 項目別収支グラフで利用可能な期間リスト
            resizeTimeout: null, // ウィンドウリサイズのデバウンス用タイマー
            latestDataDates: { // 最新データの年月日
                year: null,
                month: null,
                day: null
            },
            selectUpdateKey: 0, // select要素の強制更新用キー
            // CSVインポート関連
            showImportCSVModal: false,
            csvFile: null,
            csvImportMode: 'append',
            csvImporting: false,
            csvImportError: null,
            csvImportSuccess: null,
            // クレジットカード設定関連
            selectedCreditCardItems: [],
            showCreditCardDropdown: false,
            creditCardSettingsMessage: null,
            showCreditCardModal: false
        }
    },
    computed: {
        // 検索結果に基づいてフィルタリングされた取引
        filteredTransactions() {
            let filtered = this.transactions;

            // 資金項目によるフィルタリング（複数選択対応）
            if (this.selectedFundItems.length === 0) {
                // 何も選択されていない場合は空の結果を返す
                filtered = [];
            } else if (this.selectedFundItems.length < this.actualFundItems.length) {
                // 部分選択の場合のみフィルタリング
                filtered = filtered.filter(tx => {
                    const fundItem = tx.fundItem || tx.account;
                    return this.selectedFundItems.includes(fundItem);
                });
            }
            // 全選択の場合はフィルタリングしない（全て表示）

            // 検索クエリによるフィルタリング
            if (this.searchQuery.trim()) {
                const query = this.searchQuery.trim().toLowerCase();
                filtered = filtered.filter(tx => {
                    return (
                        tx.item.toLowerCase().includes(query) ||
                        (tx.fundItem || tx.account || '').toLowerCase().includes(query) ||
                        tx.date.includes(query) ||
                        tx.amount.toString().includes(query)
                    );
                });
            }

            return filtered;
        },
        // 選択された資金項目の表示名（デフォルトで全選択）
        selectedFundItemDisplay() {
            if (this.selectedFundItems.length === 0) {
                return '未選択';
            } else if (this.selectedFundItems.length === this.fundItemNames.filter(name => name !== 'すべて').length) {
                return 'すべて';
            } else if (this.selectedFundItems.length === 1) {
                return this.selectedFundItems[0];
            } else {
                return `${this.selectedFundItems.length}項目選択中`;
            }
        },
        // 実際の資金項目リスト（「すべて」を除く）
        actualFundItems() {
            return this.fundItemNames.filter(name => name !== 'すべて');
        },
        // 資金項目カラムを表示するかどうか（2つ以上選択されている場合のみ表示）
        shouldShowFundItemColumn() {
            return this.selectedFundItems.length >= 2;
        },
        // 収支比率グラフの期間選択肢表示テキスト
        ratioDisplayOptions() {
            const currentDate = this.ratioCurrentDate;
            return [
                { value: 'all', text: '全期間' },
                { 
                    value: 'year', 
                    text: this.ratioDisplayUnit === 'year' && currentDate ? 
                        `${currentDate.getFullYear()}年` : 
                        (this.latestDataDates.year ? `${this.latestDataDates.year}年` : '年別')
                },
                { 
                    value: 'month', 
                    text: this.ratioDisplayUnit === 'month' && currentDate ? 
                        `${currentDate.getFullYear()}年${currentDate.getMonth() + 1}月` : 
                        (this.latestDataDates.month ? this.latestDataDates.month : '月別')
                },
                { 
                    value: 'day', 
                    text: this.ratioDisplayUnit === 'day' && currentDate ? 
                        `${currentDate.getFullYear()}年${currentDate.getMonth() + 1}月${currentDate.getDate()}日` : 
                        (this.latestDataDates.day ? this.latestDataDates.day : '日別')
                }
            ];
        },
        // 項目別収支グラフの期間選択肢表示テキスト
        itemizedDisplayOptions() {
            const currentDate = this.itemizedCurrentDate;
            return [
                { value: 'all', text: '全期間' },
                { 
                    value: 'year', 
                    text: this.itemizedDisplayUnit === 'year' && currentDate ? 
                        `${currentDate.getFullYear()}年` : 
                        (this.latestDataDates.year ? `${this.latestDataDates.year}年` : '年別')
                },
                { 
                    value: 'month', 
                    text: this.itemizedDisplayUnit === 'month' && currentDate ? 
                        `${currentDate.getFullYear()}年${currentDate.getMonth() + 1}月` : 
                        (this.latestDataDates.month ? this.latestDataDates.month : '月別')
                },
                { 
                    value: 'day', 
                    text: this.itemizedDisplayUnit === 'day' && currentDate ? 
                        `${currentDate.getFullYear()}年${currentDate.getMonth() + 1}月${currentDate.getDate()}日` : 
                        (this.latestDataDates.day ? this.latestDataDates.day : '日別')
                }
            ];
        },
        // 収支比率グラフの現在期間表示
        ratioCurrentPeriodDisplay() {
            if (this.ratioDisplayUnit === 'all') return '';
            const date = this.ratioCurrentDate;
            if (this.ratioDisplayUnit === 'year') {
                return `${date.getFullYear()}年`;
            } else if (this.ratioDisplayUnit === 'month') {
                return `${date.getFullYear()}年${date.getMonth() + 1}月`;
            } else if (this.ratioDisplayUnit === 'day') {
                return `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日`;
            }
            return '';
        },
        // 項目別収支グラフの現在期間表示
        itemizedCurrentPeriodDisplay() {
            if (this.itemizedDisplayUnit === 'all') return '';
            const date = this.itemizedCurrentDate;
            if (this.itemizedDisplayUnit === 'year') {
                return `${date.getFullYear()}年`;
            } else if (this.itemizedDisplayUnit === 'month') {
                return `${date.getFullYear()}年${date.getMonth() + 1}月`;
            } else if (this.itemizedDisplayUnit === 'day') {
                return `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日`;
            }
            return '';
        },
        // 検索・フィルタリング結果に基づいて残高を再計算する
        transactionsWithRecalculatedBalance() {
            const transactions = [...this.filteredTransactions];

            if (transactions.length === 0) {
                return [];
            }

            // 日付順にソート（古い順）
            transactions.sort((a, b) => {
                const dateA = new Date(a.date);
                const dateB = new Date(b.date);
                return dateA - dateB;
            });

            // 残高を0から再計算（検索結果だけの推移として計算）
            let runningBalance = 0;
            
            // 選択された資金項目が全てクレジットカード項目かチェック
            const allSelectedAreCredit = this.selectedFundItems.length > 0 && 
                this.selectedFundItems.every(item => this.selectedCreditCardItems.includes(item));
            
            const recalculatedTransactions = transactions.map((tx) => {
                const isCreditCard = this.selectedCreditCardItems.includes(tx.account);
                
                // クレジットカード項目でも、全選択項目がクレジットカードの場合は計算に含める
                if (!isCreditCard || allSelectedAreCredit) {
                    // 現在の取引を残高計算に適用
                    const currentAmount = tx.type === 'income' ? tx.amount : -tx.amount;
                    runningBalance += currentAmount;
                }

                return {
                    ...tx,
                    balance: (isCreditCard && !allSelectedAreCredit) ? null : runningBalance
                };
            });

            return recalculatedTransactions;
        },
        currentBalance() {
            const recalculatedTransactions = this.transactionsWithRecalculatedBalance;
            if (recalculatedTransactions.length === 0) {
                return 0;
            }
            // 最新の残高を返す（日付順ソート済みなので最後の要素）
            // ただし、balance が null の場合（クレジットカード項目）は、
            // 最後の非null残高を探す
            for (let i = recalculatedTransactions.length - 1; i >= 0; i--) {
                if (recalculatedTransactions[i].balance !== null) {
                    return recalculatedTransactions[i].balance;
                }
            }
            // 全てがクレジットカード項目の場合は0を返す
            return 0;
        },
        sortedTransactions() {
            const transactionsToDisplay = [...this.transactionsWithRecalculatedBalance];
            transactionsToDisplay.sort((a, b) => {
                const dateA = new Date(a.date);
                const dateB = new Date(b.date);
                if (this.dateSortOrder === 'asc') {
                    return dateA - dateB;
                }
                return dateB - dateA;
            });
            return transactionsToDisplay;
        }
    },
    methods: {
        // ログ送信メソッド
        async logMessage(level, message, component = 'app') {
            try {
                await fetch('/api/log', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify({
                        level: level,
                        message: message,
                        component: component
                    })
                });
            } catch (error) {
                // ログ送信に失敗した場合は無視（無限ループを防ぐため）
            }
        },
        // ウィンドウリサイズ時のグラフ再描画処理
        handleWindowResize() {
            // デバウンス処理でリサイズイベントの頻度を制限
            clearTimeout(this.resizeTimeout);
            this.resizeTimeout = setTimeout(() => {
                // 各モーダルが表示されている場合のみ再描画
                if (this.showGraph) {
                    this.renderBalanceChart();
                }
                if (this.showRatioModal) {
                    this.renderRatioChart();
                }
                if (this.showItemizedModal) {
                    this.renderItemizedCharts();
                }
            }, 100); // 100ms後に実行
        },
        // 最新データの日付を更新
        updateLatestDataDates() {
            if (this.transactions.length === 0) {
                this.latestDataDates = { year: null, month: null, day: null };
                return;
            }

            // 取引データを日付でソートして最新の日付を取得
            const sortedTxs = [...this.transactions].sort((a, b) => new Date(b.date) - new Date(a.date));
            const latestDate = new Date(sortedTxs[0].date);
            
            this.latestDataDates = {
                year: latestDate.getFullYear(),
                month: `${latestDate.getFullYear()}年${latestDate.getMonth() + 1}月`,
                day: `${latestDate.getFullYear()}年${latestDate.getMonth() + 1}月${latestDate.getDate()}日`
            };
        },
        // セッションストレージから資金項目選択状態を復元
        loadSelectedFundItemsFromSession() {
            try {
                const savedSelection = sessionStorage.getItem('selectedFundItems');
                if (savedSelection) {
                    const parsed = JSON.parse(savedSelection);
                    if (Array.isArray(parsed)) {
                        this.selectedFundItems = parsed;
                        this.logMessage('debug', `資金項目選択状態を復元しました: ${parsed.length}件`, 'session');
                        return true;
                    }
                }
            } catch (error) {
                // セッションストレージエラーは無視
            }
            return false;
        },
        // セッションストレージに資金項目選択状態を保存
        saveSelectedFundItemsToSession() {
            try {
                sessionStorage.setItem('selectedFundItems', JSON.stringify(this.selectedFundItems));
                this.logMessage('debug', `資金項目選択状態を保存しました: ${this.selectedFundItems.length}件`, 'session');
            } catch (error) {
                // セッションストレージエラーは無視
            }
        },
        formatCurrency(amount) {
            return '¥' + amount.toLocaleString();
        },
        formatDateTime(dateTimeString) {
            if (!dateTimeString) return '';
            const date = new Date(dateTimeString);
            const year = date.getFullYear();
            const month = ('0' + (date.getMonth() + 1)).slice(-2);
            const day = ('0' + date.getDate()).slice(-2);
            if (dateTimeString.includes(' ') || dateTimeString.includes('T')) {
                const hours = ('0' + date.getHours()).slice(-2);
                const minutes = ('0' + date.getMinutes()).slice(-2);
                return `${year}-${month}-${day} ${hours}:${minutes}`;
            }
            return `${year}-${month}-${day}`;
        },
        getAmountCellClass(type) {
            return type === 'income' ? 'income-cell' : 'expense-cell';
        },
        getAmountInputClass(type) {
            return type === 'income' ? 'amount-input-income' : 'amount-input-expense';
        },
        formatAmount(amount, type) {
            const formattedAmount = amount.toLocaleString();
            return type === 'income' ? `+${formattedAmount}` : `-${formattedAmount}`;
        },
        toggleDateSort() {
            this.dateSortOrder = this.dateSortOrder === 'asc' ? 'desc' : 'asc';
        },
        toggleFundItem(fundItem) {
            if (this.selectedFundItems.includes(fundItem)) {
                // 選択済みの場合は削除
                this.selectedFundItems = this.selectedFundItems.filter(item => item !== fundItem);
            } else {
                // 未選択の場合は追加
                this.selectedFundItems.push(fundItem);
            }
            // 選択状態をセッションストレージに保存
            this.saveSelectedFundItemsToSession();
            this.logMessage('debug', `資金項目選択を変更しました: ${fundItem} (総数: ${this.selectedFundItems.length}件)`, 'ui');
            this.loadTransactions(); // 資金項目変更時にデータを再読み込み
        },
        toggleAllFundItems() {
            if (this.selectedFundItems.length === this.actualFundItems.length) {
                // 全選択の場合は全解除
                this.selectedFundItems = [];
            } else {
                // 部分選択の場合は全選択
                this.selectedFundItems = [...this.actualFundItems];
            }
            // 選択状態をセッションストレージに保存
            this.saveSelectedFundItemsToSession();
            const action = this.selectedFundItems.length === this.actualFundItems.length ? '全選択' : '全解除';
            this.logMessage('debug', `資金項目を${action}しました (総数: ${this.selectedFundItems.length}件)`, 'ui');
            this.loadTransactions();
        },
        isFundItemSelected(fundItem) {
            return this.selectedFundItems.includes(fundItem);
        },
        // 全取引データから利用可能な期間リストを生成
        async generateAvailablePeriods(displayUnit) {
            try {
                const response = await fetch('/api/transactions');
                const allTransactions = await response.json();
                
                // 資金項目でフィルタリング
                let filteredTxs;
                if (this.selectedFundItems.length === 0) {
                    filteredTxs = [];
                } else if (this.selectedFundItems.length < this.actualFundItems.length) {
                    filteredTxs = allTransactions.filter(t => this.selectedFundItems.includes(t.fundItem || t.account));
                } else {
                    filteredTxs = allTransactions;
                }
                
                const periodsSet = new Set();
                filteredTxs.forEach(tx => {
                    const date = new Date(tx.date);
                    let periodKey;
                    if (displayUnit === 'year') {
                        periodKey = `${date.getFullYear()}`;
                    } else if (displayUnit === 'month') {
                        periodKey = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}`;
                    } else if (displayUnit === 'day') {
                        periodKey = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
                    }
                    if (periodKey) periodsSet.add(periodKey);
                });
                
                return Array.from(periodsSet).sort();
            } catch (error) {
                return [];
            }
        },
        // グラフ用資金項目の表示テキスト（統一された選択を使用）
        getGraphFundItemDisplayText() {
            return this.selectedFundItemDisplay;
        },
        // グラフ用資金項目のドロップダウン切り替え
        toggleGraphFundItemDropdown() {
            this.showGraphFundItemDropdown = !this.showGraphFundItemDropdown;
        },
        // グラフ用資金項目の個別切り替え（統一された選択を使用）
        toggleGraphFundItem(fundItem) {
            this.toggleFundItem(fundItem);
            this.renderBalanceChart();
        },
        // グラフ用資金項目の全選択/全解除（統一された選択を使用）
        toggleAllGraphFundItems() {
            this.toggleAllFundItems();
            this.renderBalanceChart();
        },
        // 収支比率用資金項目の表示テキスト（統一された選択を使用）
        getRatioFundItemDisplayText() {
            return this.selectedFundItemDisplay;
        },
        toggleRatioFundItemDropdown() {
            this.showRatioFundItemDropdown = !this.showRatioFundItemDropdown;
        },
        toggleRatioFundItem(fundItem) {
            this.toggleFundItem(fundItem);
            this.renderRatioChart();
        },
        toggleAllRatioFundItems() {
            this.toggleAllFundItems();
            this.renderRatioChart();
        },
        // 項目別収支用資金項目の表示テキスト
        getItemizedFundItemDisplayText() {
            return this.selectedFundItemDisplay;
        },
        toggleItemizedFundItemDropdown() {
            this.showItemizedFundItemDropdown = !this.showItemizedFundItemDropdown;
        },
        toggleItemizedFundItem(fundItem) {
            this.toggleFundItem(fundItem);
            this.renderItemizedCharts();
        },
        toggleAllItemizedFundItems() {
            this.toggleAllFundItems();
            this.renderItemizedCharts();
        },
        // 収支比率グラフの期間単位変更時の処理
        async onRatioDisplayUnitChange() {
            if (this.ratioDisplayUnit !== 'all') {
                this.ratioAvailablePeriods = await this.generateAvailablePeriods(this.ratioDisplayUnit);
                if (this.ratioAvailablePeriods.length > 0) {
                    // 最新の期間を選択
                    const latestPeriod = this.ratioAvailablePeriods[this.ratioAvailablePeriods.length - 1];
                    this.setCurrentDateFromPeriod(latestPeriod, this.ratioDisplayUnit, 'ratio');
                }
            }
            this.renderRatioChart();
        },
        // 項目別収支グラフの期間単位変更時の処理
        async onItemizedDisplayUnitChange() {
            if (this.itemizedDisplayUnit !== 'all') {
                this.itemizedAvailablePeriods = await this.generateAvailablePeriods(this.itemizedDisplayUnit);
                if (this.itemizedAvailablePeriods.length > 0) {
                    // 最新の期間を選択
                    const latestPeriod = this.itemizedAvailablePeriods[this.itemizedAvailablePeriods.length - 1];
                    this.setCurrentDateFromPeriod(latestPeriod, this.itemizedDisplayUnit, 'itemized');
                }
            }
            this.renderItemizedCharts();
        },
        // 期間文字列から日付オブジェクトを設定
        setCurrentDateFromPeriod(periodStr, displayUnit, chartType) {
            let date;
            if (displayUnit === 'year') {
                date = new Date(parseInt(periodStr), 0, 1);
            } else if (displayUnit === 'month') {
                const [year, month] = periodStr.split('-');
                date = new Date(parseInt(year), parseInt(month) - 1, 1);
            } else if (displayUnit === 'day') {
                const [year, month, day] = periodStr.split('-');
                date = new Date(parseInt(year), parseInt(month) - 1, parseInt(day));
            } else {
                date = new Date();
            }
            
            if (chartType === 'ratio') {
                this.ratioCurrentDate = date;
            } else {
                this.itemizedCurrentDate = date;
            }
            
            // select要素の表示を強制更新
            this.selectUpdateKey++;
        },
        // 収支比率グラフの期間移動
        async navigateRatioPeriod(direction) {
            if (this.ratioAvailablePeriods.length === 0) return;
            
            const currentPeriodStr = this.getCurrentPeriodString(this.ratioCurrentDate, this.ratioDisplayUnit);
            const currentIndex = this.ratioAvailablePeriods.indexOf(currentPeriodStr);
            const newIndex = currentIndex + direction;
            
            if (newIndex >= 0 && newIndex < this.ratioAvailablePeriods.length) {
                const newPeriod = this.ratioAvailablePeriods[newIndex];
                this.setCurrentDateFromPeriod(newPeriod, this.ratioDisplayUnit, 'ratio');
                this.renderRatioChart();
            }
        },
        // 項目別収支グラフの期間移動
        async navigateItemizedPeriod(direction) {
            if (this.itemizedAvailablePeriods.length === 0) return;
            
            const currentPeriodStr = this.getCurrentPeriodString(this.itemizedCurrentDate, this.itemizedDisplayUnit);
            const currentIndex = this.itemizedAvailablePeriods.indexOf(currentPeriodStr);
            const newIndex = currentIndex + direction;
            
            if (newIndex >= 0 && newIndex < this.itemizedAvailablePeriods.length) {
                const newPeriod = this.itemizedAvailablePeriods[newIndex];
                this.setCurrentDateFromPeriod(newPeriod, this.itemizedDisplayUnit, 'itemized');
                this.renderItemizedCharts();
            }
        },
        // 日付から期間文字列を取得
        getCurrentPeriodString(date, displayUnit) {
            if (displayUnit === 'year') {
                return `${date.getFullYear()}`;
            } else if (displayUnit === 'month') {
                return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}`;
            } else if (displayUnit === 'day') {
                return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
            }
            return '';
        },
        // 収支比率グラフの期間移動可否判定
        canNavigateRatioPeriod(direction) {
            if (this.ratioAvailablePeriods.length === 0) return false;
            
            const currentPeriodStr = this.getCurrentPeriodString(this.ratioCurrentDate, this.ratioDisplayUnit);
            const currentIndex = this.ratioAvailablePeriods.indexOf(currentPeriodStr);
            const newIndex = currentIndex + direction;
            
            return newIndex >= 0 && newIndex < this.ratioAvailablePeriods.length;
        },
        // 項目別収支グラフの期間移動可否判定
        canNavigateItemizedPeriod(direction) {
            if (this.itemizedAvailablePeriods.length === 0) return false;
            
            const currentPeriodStr = this.getCurrentPeriodString(this.itemizedCurrentDate, this.itemizedDisplayUnit);
            const currentIndex = this.itemizedAvailablePeriods.indexOf(currentPeriodStr);
            const newIndex = currentIndex + direction;
            
            return newIndex >= 0 && newIndex < this.itemizedAvailablePeriods.length;
        },
        toggleAccountDropdown() {
            this.showAccountDropdown = !this.showAccountDropdown;
        },
        positionDropdown() {
            // CSSのposition: absoluteとtop: 100%, left: 0を使用するため、
            // JavaScriptでの位置調整は不要
            // ドロップダウンは自動的に.project-selectorの真下に表示される
        },
        handleClickOutside(event) {
            const projectSelector = document.querySelector('.project-selector');
            if (projectSelector && !projectSelector.contains(event.target)) {
                this.showAccountDropdown = false;
            }

            // 資金項目ドロップダウンのクリック外処理
            const fundItemGroup = document.querySelector('.funditem-input-group');
            if (fundItemGroup && !fundItemGroup.contains(event.target)) {
                this.showFundItemDropdown = false;
            }

            // グラフ用資金項目ドロップダウンのクリック外処理
            const graphMultiSelect = event.target.closest('.multi-select-wrapper');
            if (!graphMultiSelect || !event.target.closest('[data-graph-fund-items]')) {
                this.showGraphFundItemDropdown = false;
            }

            // 収支比率用資金項目ドロップダウンのクリック外処理
            if (!graphMultiSelect || !event.target.closest('[data-ratio-fund-items]')) {
                this.showRatioFundItemDropdown = false;
            }

            // 項目別収支用資金項目ドロップダウンのクリック外処理
            if (!graphMultiSelect || !event.target.closest('[data-itemized-fund-items]')) {
                this.showItemizedFundItemDropdown = false;
            }

            // クレジットカード設定ドロップダウンのクリック外処理
            if (!graphMultiSelect || !event.target.closest('[data-credit-card-items]')) {
                this.showCreditCardDropdown = false;
            }
        },
        async loadTransactions() {
            try {
                this.loading = true;
                // 検索パラメータを構築
                const params = new URLSearchParams();
                // 検索とフィルタリングはフロントエンドで行うため、全データを取得
                // 複数選択対応のため、常に全データを取得してフロントエンドでフィルタ
                const url = `/api/transactions${params.toString() ? '?' + params.toString() : ''}`;
                const response = await fetch(url);
                if (!response.ok) {
                    throw new Error('データの取得に失敗しました');
                }
                this.transactions = await response.json();
                // 取引データ読み込み後に最新日付を更新
                this.updateLatestDataDates();
                this.logMessage('info', `取引データを読み込みました: ${this.transactions.length}件`, 'transactions');
            } catch (error) {
                this.logMessage('error', '取引データの読み込みエラー: ' + error.toString(), 'transactions');
                alert('データの読み込みに失敗しました。');
            } finally {
                this.loading = false;
            }
        },
        async loadFundItems() {
            try {
                const response = await fetch('/api/accounts');
                if (!response.ok) {
                    throw new Error('資金項目データの取得に失敗しました');
                }
                const fundItems = await response.json();
                // 「すべて」を先頭に追加
                this.fundItemNames = ['すべて', ...fundItems];
                this.logMessage('info', `資金項目データを読み込みました: ${fundItems.length}件`, 'accounts');
                // セッションストレージから選択状態を復元、失敗時はデフォルトで全選択
                if (!this.loadSelectedFundItemsFromSession()) {
                    this.selectedFundItems = [...fundItems];
                }
            } catch (error) {
                this.logMessage('error', '資金項目データの読み込みエラー: ' + error.toString(), 'accounts');
                alert('資金項目データの読み込みに失敗しました。');
                // エラーの場合はデフォルト値を設定
                this.fundItemNames = ['すべて'];
            }
        },
        async loadItemNames(account = null) {
            try {
                let url = '/api/items';
                if (account) {
                    url += `?account=${encodeURIComponent(account)}`;
                }
                const response = await fetch(url);
                if (!response.ok) {
                    throw new Error('項目データの取得に失敗しました');
                }
                const items = await response.json();
                this.itemNames = items;
                const accountInfo = account ? `(資金項目: ${account})` : '(全体)';
                this.logMessage('info', `項目データを読み込みました: ${items.length}件 ${accountInfo}`, 'items');
            } catch (error) {
                this.logMessage('error', '項目データの読み込みエラー: ' + error.toString(), 'items');
                this.itemNames = [];
            }
        },
        async backupToCSV() {
            try {
                const response = await fetch('/api/backup_csv');
                if (!response.ok) {
                    throw new Error('バックアップに失敗しました');
                }
                const blob = await response.blob();
                const contentDisposition = response.headers.get('Content-Disposition');
                let filename = 'transactions_backup.csv';
                if (contentDisposition && contentDisposition.includes('filename=')) {
                    filename = contentDisposition.split('filename=')[1].split(';')[0].replace(/"/g, '').trim();
                }
                const url = window.URL.createObjectURL(blob);
                const a = document.createElement('a');
                a.href = url;
                a.download = filename;
                document.body.appendChild(a);
                a.click();
                a.remove();
                window.URL.revokeObjectURL(url);
                this.logMessage('info', `CSVバックアップをダウンロードしました: ${filename}`, 'backup');
                this.showMenu = false;
            } catch (error) {
                this.logMessage('error', 'CSVバックアップエラー: ' + error.toString(), 'backup');
                alert('バックアップに失敗しました。');
            }
        },
        async downloadLog() {
            try {
                const response = await fetch('/api/download_log');
                if (!response.ok) {
                    const errorData = await response.json();
                    throw new Error(errorData.error || 'ログファイルのダウンロードに失敗しました');
                }
                const blob = await response.blob();
                const contentDisposition = response.headers.get('Content-Disposition');
                let filename = 'money_tracker.log';
                if (contentDisposition && contentDisposition.includes('filename=')) {
                    filename = contentDisposition.split('filename=')[1].split(';')[0].replace(/"/g, '').trim();
                }
                const url = window.URL.createObjectURL(blob);
                const a = document.createElement('a');
                a.href = url;
                a.download = filename;
                document.body.appendChild(a);
                a.click();
                a.remove();
                window.URL.revokeObjectURL(url);
                this.logMessage('info', `ログファイルをダウンロードしました: ${filename}`, 'log-download');
                this.showMenu = false;
            } catch (error) {
                this.logMessage('error', 'ログファイルダウンロードエラー: ' + error.toString(), 'log-download');
                alert('ログファイルのダウンロードに失敗しました: ' + error.message);
            }
        },
        openRatioModal() {
            this.showMenu = false;
            this.showRatioModal = true;
            this.logMessage('info', '収支比率グラフモーダルを表示しました', 'ui');
            this.$nextTick(() => { this.renderRatioChart(); });
        },
        hideRatioModal() { 
            this.showRatioModal = false; 
            if (this.ratioChartInstance) this.ratioChartInstance.destroy(); 
            this.logMessage('debug', '収支比率グラフモーダルを閉じました', 'ui');
        },
        openItemizedModal() {
            this.showMenu = false;
            this.showItemizedModal = true;
            this.logMessage('info', '項目別収支グラフモーダルを表示しました', 'ui');
            this.$nextTick(() => { this.renderItemizedCharts(); });
        },
        hideItemizedModal() { 
            this.showItemizedModal = false; 
            if (this.incomeItemChartInstance) this.incomeItemChartInstance.destroy(); 
            if (this.expenseItemChartInstance) this.expenseItemChartInstance.destroy(); 
            this.logMessage('debug', '項目別収支グラフモーダルを閉じました', 'ui');
        },
        async renderRatioChart() {
            // 既存チャートを破棄
            if (this.ratioChartInstance) {
                this.ratioChartInstance.destroy();
                this.ratioChartInstance = null;
            }
            // canvasサイズを円グラフに適したサイズに調整
            const wrapper = document.querySelector('.graph-scroll-wrapper');
            const canvas = document.getElementById('ratioChart');
            if (wrapper && canvas) {
                const availableWidth = Math.max(wrapper.clientWidth - 30, 400);
                const availableHeight = Math.max(wrapper.clientHeight - 30, 300);
                // 円グラフは正方形が理想的なので、幅と高さの小さい方を基準にサイズを決定
                const size = Math.min(availableWidth, availableHeight);
                canvas.width = size;
                canvas.height = size;
                canvas.style.width = size + 'px';
                canvas.style.height = size + 'px';
            }
            const ctx = canvas.getContext('2d');
            // 全取引を再取得
            let allTxs = [];
            try {
                const res = await fetch('/api/transactions');
                allTxs = await res.json();
            } catch (e) {
                this.logMessage('error', '取引データ取得エラー: ' + e.toString(), 'transactions');
            }
            // 選択中の資金項目でフィルタ（統一された選択を使用）
            let filteredTxs;
            if (this.selectedFundItems.length === 0) {
                // 何も選択されていない場合は空のデータ
                filteredTxs = [];
            } else if (this.selectedFundItems.length < this.actualFundItems.length) {
                // 部分選択の場合のみフィルタリング
                filteredTxs = allTxs.filter(t => this.selectedFundItems.includes(t.fundItem || t.account));
            } else {
                // 全選択の場合は全て表示
                filteredTxs = allTxs;
            }
            
            // クレジットカード項目の処理
            if (filteredTxs.length > 0) {
                // 選択された資金項目が全てクレジットカード項目かチェック
                const allSelectedAreCredit = this.selectedFundItems.length > 0 && 
                    this.selectedFundItems.every(item => this.selectedCreditCardItems.includes(item));
                
                // 通常項目とクレジットカード項目が混在している場合、クレジットカード項目を除外
                if (!allSelectedAreCredit) {
                    filteredTxs = filteredTxs.filter(t => !this.selectedCreditCardItems.includes(t.fundItem || t.account));
                }
            }
            
            // 期間選択に基づいてデータをさらにフィルタ
            if (this.ratioDisplayUnit !== 'all') {
                const selectedDate = this.ratioCurrentDate;
                const selectedYear = selectedDate.getFullYear();
                const selectedMonth = selectedDate.getMonth();
                const selectedDay = selectedDate.getDate();
                
                filteredTxs = filteredTxs.filter(t => {
                    const txDate = new Date(t.date);
                    if (this.ratioDisplayUnit === 'year') {
                        return txDate.getFullYear() === selectedYear;
                    } else if (this.ratioDisplayUnit === 'month') {
                        return txDate.getFullYear() === selectedYear && txDate.getMonth() === selectedMonth;
                    } else if (this.ratioDisplayUnit === 'day') {
                        return txDate.getFullYear() === selectedYear && 
                               txDate.getMonth() === selectedMonth && 
                               txDate.getDate() === selectedDay;
                    }
                    return true;
                });
            }
            
            const totalIncome = filteredTxs.filter(t => t.type === 'income').reduce((sum, t) => sum + t.amount, 0);
            const totalExpense = filteredTxs.filter(t => t.type === 'expense').reduce((sum, t) => sum + t.amount, 0);
            const data = {
                labels: ['収入','支出'],
                datasets: [{ data:[totalIncome,totalExpense], backgroundColor:['#4caf50','#f44336'] }]
            };
            const options = {
                responsive: true,
                maintainAspectRatio: true,
                aspectRatio: 1, // 正方形を強制
                plugins: {
                    legend: {
                        position: 'bottom'
                    }
                }
            };
            this.ratioChartInstance = new Chart(ctx, { type:'pie', data, options });
        },
        async renderItemizedCharts() {
            // destroy existing
            if (this.incomeItemChartInstance) { this.incomeItemChartInstance.destroy(); this.incomeItemChartInstance = null; }
            if (this.expenseItemChartInstance) { this.expenseItemChartInstance.destroy(); this.expenseItemChartInstance = null; }
            // canvasサイズを2つの円グラフ（横並び）に適したサイズに調整
            // 収支比率グラフのサイズ計算を参考にして、2つのグラフ用に調整
            const wrapper = document.querySelector('.graph-scroll-wrapper');
            const incomeCanvas = document.getElementById('incomeItemChart');
            const expenseCanvas = document.getElementById('expenseItemChart');
            if (wrapper && incomeCanvas && expenseCanvas) {
                // 収支比率グラフと同じ方式でサイズ計算（2つのグラフ用に調整）
                const availableWidth = Math.max(wrapper.clientWidth - 60, 600); // 少し大きめの最小幅
                const availableHeight = Math.max(wrapper.clientHeight - 80, 400); // パディング分を考慮
                
                // 各グラフのタイトル（h4）の高さを計算に含める
                // HTMLで設定した font-size: 1.1em + margin: 0 0 10px 0 を考慮
                const titleHeight = Math.ceil(1.1 * 16) + 10; // 1.1em ≈ 17.6px + margin-bottom 10px
                
                // 2つのグラフを横並びにするため、利用可能幅を2で割る（ギャップ20pxを考慮）
                const widthPerChart = Math.floor((availableWidth - 20) / 2);
                
                // 円グラフの利用可能高さからタイトル分を差し引く
                const heightAvailableForChart = availableHeight - titleHeight;
                
                // 円グラフは正方形が理想的なので、幅と高さの小さい方を基準にサイズを決定
                const sizePerChart = Math.min(widthPerChart, heightAvailableForChart);
                
                this.logMessage('debug', `Available dimensions: ${availableWidth} x ${availableHeight}`, 'chart-debug');
                this.logMessage('debug', `Title height: ${titleHeight}`, 'chart-debug');
                this.logMessage('debug', `Width per chart: ${widthPerChart}`, 'chart-debug');
                this.logMessage('debug', `Height available for chart: ${heightAvailableForChart}`, 'chart-debug');
                this.logMessage('debug', `Final chart size: ${sizePerChart}`, 'chart-debug');
                
                [incomeCanvas, expenseCanvas].forEach(canvas => {
                    canvas.width = sizePerChart;
                    canvas.height = sizePerChart;
                    canvas.style.width = sizePerChart + 'px';
                    canvas.style.height = sizePerChart + 'px';
                });
            }
            // fetch fresh transactions
            let allTxs = [];
            try {
                const res = await fetch('/api/transactions'); allTxs = await res.json();
            } catch (e) { this.logMessage('error', '取引データ取得エラー: ' + e.toString(), 'transactions'); }
            // filter by selected fund item（統一された選択を使用）
            let filtered;
            if (this.selectedFundItems.length === 0) {
                // 何も選択されていない場合は空のデータ
                filtered = [];
            } else if (this.selectedFundItems.length < this.actualFundItems.length) {
                // 部分選択の場合のみフィルタリング
                filtered = allTxs.filter(t => this.selectedFundItems.includes(t.fundItem || t.account));
            } else {
                // 全選択の場合は全て表示
                filtered = allTxs;
            }
            
            // クレジットカード項目の処理
            if (filtered.length > 0) {
                // 選択された資金項目が全てクレジットカード項目かチェック
                const allSelectedAreCredit = this.selectedFundItems.length > 0 && 
                    this.selectedFundItems.every(item => this.selectedCreditCardItems.includes(item));
                
                // 通常項目とクレジットカード項目が混在している場合、クレジットカード項目を除外
                if (!allSelectedAreCredit) {
                    filtered = filtered.filter(t => !this.selectedCreditCardItems.includes(t.fundItem || t.account));
                }
            }
            
            // 期間選択に基づいてデータをさらにフィルタ
            if (this.itemizedDisplayUnit !== 'all') {
                const selectedDate = this.itemizedCurrentDate;
                const selectedYear = selectedDate.getFullYear();
                const selectedMonth = selectedDate.getMonth();
                const selectedDay = selectedDate.getDate();
                
                filtered = filtered.filter(t => {
                    const txDate = new Date(t.date);
                    if (this.itemizedDisplayUnit === 'year') {
                        return txDate.getFullYear() === selectedYear;
                    } else if (this.itemizedDisplayUnit === 'month') {
                        return txDate.getFullYear() === selectedYear && txDate.getMonth() === selectedMonth;
                    } else if (this.itemizedDisplayUnit === 'day') {
                        return txDate.getFullYear() === selectedYear && 
                               txDate.getMonth() === selectedMonth && 
                               txDate.getDate() === selectedDay;
                    }
                    return true;
                });
            }
            // aggregate by item
            const incomeItems = {}, expenseItems = {};
            filtered.forEach(t => {
                const key = t.item || '未指定';
                if (t.type==='income') incomeItems[key] = (incomeItems[key]||0) + t.amount;
                else expenseItems[key] = (expenseItems[key]||0) + t.amount;
            });
            // sort entries by descending value
            const inEntries = Object.entries(incomeItems).sort((a,b) => b[1] - a[1]);
            const exEntries = Object.entries(expenseItems).sort((a,b) => b[1] - a[1]);
            const inLabels = inEntries.map(([key]) => key);
            const inData = inEntries.map(([_, val]) => val);
            const exLabels = exEntries.map(([key]) => key);
            const exData = exEntries.map(([_, val]) => val);
            // 収支比率グラフと同じChart.js設定を使用
            const chartOptions = { 
                responsive: true,
                maintainAspectRatio: true,
                aspectRatio: 1, // 正方形を強制
                plugins: { 
                    legend: { 
                        display: false // レジェンド非表示、ホバー時のツールチップは維持
                    },
                    tooltip: {
                        enabled: true // ホバー時のツールチップを有効
                    }
                } 
            };
            const ctxIn = document.getElementById('incomeItemChart').getContext('2d');
            this.incomeItemChartInstance = new Chart(ctxIn, {
                type:'doughnut', 
                data:{ labels:inLabels, datasets:[{ data:inData, backgroundColor:inLabels.map((_,i)=>`hsl(${i*40%360},70%,50%)`)}]},
                options: chartOptions
            });
            const ctxEx = document.getElementById('expenseItemChart').getContext('2d');
            this.expenseItemChartInstance = new Chart(ctxEx, {
                type:'doughnut',
                data:{ labels:exLabels, datasets:[{ data:exData, backgroundColor:exLabels.map((_,i)=>`hsl(${i*40%360},70%,50%)`)}]},
                options: chartOptions
            });
        },
        onSearchInput() {
            // 検索はフロントエンドで行うため、すぐに結果を更新
            // デバウンスは不要（計算処理が軽いため）
        },
        async showAddModal() {
            // 常に最新の資金項目リストを取得
            await this.loadFundItems();
            const today = new Date();
            const year = today.getFullYear();
            const month = String(today.getMonth() + 1).padStart(2, '0');
            const day = String(today.getDate()).padStart(2, '0');
            this.newTransaction.date = `${year}-${month}-${day}`;
            this.newTransaction.time = today.toTimeString().slice(0, 5);
            // 「すべて」以外の最初の資金項目を初期値に
            const firstFundItem = this.fundItemNames.find(name => name !== 'すべて') || '';
            this.newTransaction.fundItem = firstFundItem;
            // 選択された資金項目の項目名を取得
            if (firstFundItem) {
                await this.loadItemNames(firstFundItem);
            }
            this.newTransaction.item = '';
            this.newTransaction.type = 'expense';
            this.newTransaction.amount = '';
            this.showAddTransactionModal = true;
        },
        async onEditTransaction(transaction) {
            // 編集用にモーダルを開く
            await this.loadFundItems();
            this.isEditMode = true;
            this.editTransactionId = transaction.id;
            // 日付・時刻分割
            let date = '', time = '';
            if (transaction.date.includes(' ')) {
                [date, time] = transaction.date.split(' ');
                time = time.slice(0,5); // HH:MM
            } else {
                date = transaction.date;
                time = '';
            }
            this.newTransaction = {
                fundItem: transaction.fundItem || transaction.account,
                date: date,
                time: time,
                item: transaction.item,
                type: transaction.type,
                amount: transaction.amount.toString()
            };
            // 編集対象の資金項目の項目名を取得
            if (this.newTransaction.fundItem) {
                await this.loadItemNames(this.newTransaction.fundItem);
            }
            this.showAddTransactionModal = true;
        },
        hideAddModal() {
            this.showAddTransactionModal = false;
            this.showFundItemDropdown = false;
            this.isEditMode = false;
            this.editTransactionId = null;
        },
        // 資金項目ドロップダウンの表示/非表示を切り替え
        toggleFundItemDropdown() {
            this.showFundItemDropdown = !this.showFundItemDropdown;
        },
        // 資金項目を選択
        async selectFundItemInModal(fundItem) {
            this.newTransaction.fundItem = fundItem;
            this.showFundItemDropdown = false;
            // 選択された資金項目の項目名を再取得
            await this.loadItemNames(fundItem);
            // 項目をリセット（新しい資金項目の項目名に変わったため）
            this.newTransaction.item = '';
        },
        // 資金項目入力フィールドのクリック処理
        onFundItemInputClick() {
            this.showFundItemDropdown = true;
        },
        // 全角数字を半角数字に変換する関数
        convertToHalfWidth(str) {
            return str.replace(/[０-９]/g, function(s) {
                return String.fromCharCode(s.charCodeAt(0) - 0xFEE0);
            });
        },
        // 数字以外の文字を除去する関数
        filterNumericOnly(str) {
            // 全角・半角の数字とカンマのみを許可
            return str.replace(/[^0-9０-９,，]/g, '');
        },
        // 金額欄の入力変換イベント
        onAmountInput(event) {
            let value = event.target.value;

            // 数字以外の文字を除去
            value = this.filterNumericOnly(value);

            // 全角数字を半角数字に変換
            value = this.convertToHalfWidth(value);

            // カンマを除去して純粋な数字のみにする
            const numericValue = value.replace(/[,，]/g, '');

            // 値を更新
            this.newTransaction.amount = numericValue;

            // 入力欄の値も同期（カーソル位置を保持）
            const cursorPos = event.target.selectionStart;
            event.target.value = numericValue;

            // カーソル位置を調整（フィルタリングで文字が削除された場合に対応）
            const newPos = Math.min(cursorPos, numericValue.length);
            setTimeout(() => {
                event.target.setSelectionRange(newPos, newPos);
            }, 0);
        },
        // 金額欄のキーダウンイベント（無効なキーを防ぐ）
        onAmountKeydown(event) {
            const key = event.key;

            // 許可するキー
            const allowedKeys = [
                'Backspace', 'Delete', 'ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown',
                'Home', 'End', 'Tab', 'Escape', 'Enter'
            ];

            // Ctrl/Cmd + キーの組み合わせを許可（コピー、ペースト、全選択など）
            if (event.ctrlKey || event.metaKey) {
                return true;
            }

            // 許可されたキーの場合は通す
            if (allowedKeys.includes(key)) {
                return true;
            }

            // 半角数字
            if (key >= '0' && key <= '9') {
                return true;
            }

            // 全角数字
            if (key >= '０' && key <= '９') {
                return true;
            }

            // それ以外のキーは無効
            event.preventDefault();
            return false;
        },
        // 金額欄のペーストイベント
        onAmountPaste(event) {
            event.preventDefault();

            // クリップボードからデータを取得
            let pastedData = '';
            if (event.clipboardData) {
                pastedData = event.clipboardData.getData('text');
            } else if (window.clipboardData) {
                // IE対応（型エラーを避けるためtry-catchで囲む）
                try {
                    pastedData = window.clipboardData.getData('text');
                } catch (e) {
                    pastedData = '';
                }
            }

            // 数字以外を除去して処理
            const filteredData = this.filterNumericOnly(pastedData);
            const convertedData = this.convertToHalfWidth(filteredData);
            const numericData = convertedData.replace(/[,，]/g, '');

            // 現在の入力欄の値と組み合わせる
            const input = event.target;
            const start = input.selectionStart;
            const end = input.selectionEnd;
            const currentValue = this.newTransaction.amount || '';

            // 新しい値を作成
            const newValue = currentValue.substring(0, start) + numericData + currentValue.substring(end);

            // 値を更新
            this.newTransaction.amount = newValue;

            // カーソル位置を調整
            setTimeout(() => {
                const newPos = start + numericData.length;
                input.setSelectionRange(newPos, newPos);
            }, 0);
        },
        // 新しい資金項目かどうかを判定
        isNewFundItem(fundItemName) {
            return fundItemName && !this.fundItemNames.includes(fundItemName);
        },
        isNewItem(itemName) {
            // 現在選択されている資金項目内での重複チェック
            return itemName && !this.itemNames.includes(itemName);
        },
        async addTransaction() {
            try {
                // バリデーション（時刻は除外）
                if (!this.newTransaction.fundItem || !this.newTransaction.date ||
                    !this.newTransaction.item || !this.newTransaction.amount) {
                    alert('日付、資金項目、項目、金額は必須項目です。');
                    return;
                }

                // 金額の変換と検証
                const convertedAmount = this.convertToHalfWidth(this.newTransaction.amount);
                const amount = parseInt(convertedAmount.replace(/[^\d]/g, ''));
                if (isNaN(amount) || amount <= 0) {
                    alert('金額は正の数値を入力してください。');
                    return;
                }

                // 新しい資金項目または項目の場合は確認
                let confirmMessage = '';
                if (this.isNewFundItem(this.newTransaction.fundItem)) {
                    confirmMessage = `「${this.newTransaction.fundItem}」は新しい資金項目です。作成しますか？`;
                } else if (this.isNewItem(this.newTransaction.item)) {
                    confirmMessage = `「${this.newTransaction.item}」は新しい項目です。作成しますか？`;
                }
                if (confirmMessage && !confirm(confirmMessage)) {
                    return;
                }

                // APIに送信するデータを準備
                const transactionData = {
                    account: this.newTransaction.fundItem, // バックエンドのDBフィールドは'account'
                    date: this.newTransaction.date,
                    time: this.newTransaction.time,
                    item: this.newTransaction.item,
                    type: this.newTransaction.type,
                    amount: amount
                };

                const response = await fetch('/api/transactions', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify(transactionData)
                });

                if (!response.ok) {
                    const error = await response.json();
                    throw new Error(error.error || '取引の追加に失敗しました');
                }

                const result = await response.json();
                this.logMessage('info', `取引を追加しました: ${this.newTransaction.item} (金額: ${amount}円)`, 'add-transaction');
                alert(result.message);

                // モーダルを閉じて、データを再読み込み
                this.hideAddModal();
                await this.loadFundItems(); // 新しい資金項目が追加された可能性があるため
                // 新しい項目が追加された可能性があるため、現在の資金項目の項目名を再取得
                if (this.newTransaction.fundItem) {
                    await this.loadItemNames(this.newTransaction.fundItem);
                }
                await this.loadTransactions();

            } catch (error) {
                this.logMessage('error', '取引追加エラー: ' + error.toString(), 'add-transaction');
                alert(`エラー: ${error.message}`);
            }
        },
        async addOrUpdateTransaction() {
            // 追加・編集を分岐
            if (this.isEditMode) {
                await this.updateTransaction();
            } else {
                await this.addTransaction();
            }
        },
        async updateTransaction() {
            try {
                if (!this.newTransaction.fundItem || !this.newTransaction.date ||
                    !this.newTransaction.item || !this.newTransaction.amount) {
                    alert('日付、資金項目、項目、金額は必須項目です。');
                    return;
                }
                const convertedAmount = this.convertToHalfWidth(this.newTransaction.amount);
                const amount = parseInt(convertedAmount.replace(/[^\d]/g, ''));
                if (isNaN(amount) || amount <= 0) {
                    alert('金額は正の数値を入力してください。');
                    return;
                }
                const transactionData = {
                    account: this.newTransaction.fundItem,
                    date: this.newTransaction.date,
                    time: this.newTransaction.time,
                    item: this.newTransaction.item,
                    type: this.newTransaction.type,
                    amount: amount
                };
                const response = await fetch(`/api/transactions/${this.editTransactionId}`, {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(transactionData)
                });
                if (!response.ok) {
                    const error = await response.json();
                    throw new Error(error.error || '取引の編集に失敗しました');
                }
                const result = await response.json();
                this.logMessage('info', `取引を編集しました: ${this.newTransaction.item} (金額: ${amount}円)`, 'edit-transaction');
                alert(result.message);
                this.hideAddModal();
                await this.loadFundItems();
                // 編集で項目が変更された可能性があるため、現在の資金項目の項目名を再取得
                if (this.newTransaction.fundItem) {
                    await this.loadItemNames(this.newTransaction.fundItem);
                }
                await this.loadTransactions();
            } catch (error) {
                this.logMessage('error', '取引編集エラー: ' + error.toString(), 'edit-transaction');
                alert(`エラー: ${error.message}`);
            }
        },
        async onDeleteTransaction() {
            if (!this.isEditMode || !this.editTransactionId) return;
            if (!confirm('本当にこの取引を削除しますか？')) return;
            try {
                const response = await fetch(`/api/transactions/${this.editTransactionId}`, {
                    method: 'DELETE'
                });
                if (!response.ok) {
                    const error = await response.json();
                    throw new Error(error.error || '削除に失敗しました');
                }
                const result = await response.json();
                this.logMessage('info', '取引を削除しました', 'delete-transaction');
                alert(result.message);
                this.hideAddModal();
                await this.loadFundItems();
                // 削除で項目リストが変更された可能性があるため
                // モーダルが開いている場合は現在の資金項目の項目名を再取得
                if (this.showAddTransactionModal && this.newTransaction.fundItem) {
                    await this.loadItemNames(this.newTransaction.fundItem);
                }
                await this.loadTransactions();
            } catch (error) {
                this.logMessage('error', '取引削除エラー: ' + error.toString(), 'delete-transaction');
                alert(`エラー: ${error.message}`);
            }
        },
        toggleMenu() {
            this.showMenu = !this.showMenu;
        },
        showGraphModal() {
            this.showGraph = true;
            this.showMenu = false;
            this.logMessage('info', '残高推移グラフモーダルを表示しました', 'ui');
            this.$nextTick(() => {
                this.renderBalanceChart();
            });
        },
        hideGraphModal() {
            this.showGraph = false;
            this.logMessage('debug', '残高推移グラフモーダルを閉じました', 'ui');
        },
        async logout() {
            if (!confirm('ログアウトしますか？')) {
                return;
            }
            
            try {
                const response = await fetch('/api/logout', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json'
                    }
                });
                
                const data = await response.json();
                
                if (response.ok && data.success) {
                    // ログアウト成功 - ログイン画面にリダイレクト
                    window.location.href = '/login';
                } else {
                    alert('ログアウトに失敗しました');
                }
            } catch (error) {
                this.logMessage('error', 'Logout error: ' + error.toString(), 'auth');
                alert('ネットワークエラーが発生しました');
            }
        },
        async renderBalanceChart() {
            // 残高推移グラフの描画処理（クレジットカード項目フィルタリング対応）
            // 新しいAPI /api/balance_history_filtered を使用して、混在選択時の線途切れ問題を解決
            // 非同期処理でデータを取得し、適切なエラーハンドリングを実装
            
            // 既存のグラフがあれば破棄
            if (this._balanceChartInstance) {
                this._balanceChartInstance.destroy();
            }
            // グラフの親要素サイズにcanvasを合わせる
            const wrapper = document.querySelector('.graph-scroll-wrapper');
            const canvas = document.getElementById('balanceChart');
            if (wrapper && canvas) {
                // スクロールバー分を考慮して少し余裕を持たせる
                const w = Math.max(wrapper.clientWidth - 15, 800); // 最小幅を800pxに維持、余白を減らす
                const h = Math.max(wrapper.clientHeight - 15, 350); // 最小高さを350pxに拡大、余白を減らす
                canvas.width = w;
                canvas.height = h;
                canvas.style.width = w + 'px';
                canvas.style.height = h + 'px';
            }
            
            // 選択された資金項目が空の場合は空のグラフを表示
            if (this.selectedFundItems.length === 0) {
                const ctx = canvas.getContext('2d');
                this._balanceChartInstance = new Chart(ctx, {
                    type: 'line',
                    data: {
                        labels: [],
                        datasets: [{
                            label: '残高',
                            data: [],
                            borderColor: 'rgba(54, 162, 235, 1)',
                            backgroundColor: 'rgba(54, 162, 235, 0.1)',
                            fill: true,
                            tension: 0.2,
                            pointRadius: 2
                        }]
                    },
                    options: {
                        responsive: false,
                        maintainAspectRatio: false,
                        plugins: {
                            legend: { display: false },
                            title: { display: false }
                        },
                        scales: {
                            x: { 
                                title: { display: true, text: this.graphDisplayUnit==='month' ? '年月' : (this.graphDisplayUnit==='year' ? '年' : '日付') },
                                grid: { display: true }
                            },
                            y: { 
                                title: { display: true, text: '残高(円)' }, 
                                beginAtZero: true,
                                grid: { display: true }
                            }
                        },
                        interaction: {
                            intersect: false,
                            mode: 'index'
                        }
                    }
                });
                return;
            }

            try {
                // クレジットカード項目フィルタリング対応API から残高履歴データを取得
                // /api/balance_history_filtered：選択された資金項目に応じて、
                // クレジットカード項目の混在時は適切にフィルタリングされたデータを返す
                const params = new URLSearchParams();
                this.selectedFundItems.forEach(item => {
                    params.append('fund_items', item);
                });
                
                const response = await fetch(`/api/balance_history_filtered?${params}`, {
                    method: 'GET',
                    headers: {
                        'Content-Type': 'application/json',
                    }
                });
                
                if (!response.ok) {
                    throw new Error('残高履歴データの取得に失敗しました');
                }
                
                const balanceData = await response.json();
                this.logMessage('debug', `残高履歴グラフ用データを取得: ${balanceData.accounts?.length || 0}口座, ${balanceData.dates?.length || 0}日分`, 'balance_chart');
                
                // データが空の場合は空のグラフを表示
                if (!balanceData.dates || balanceData.dates.length === 0) {
                    const ctx = canvas.getContext('2d');
                    this._balanceChartInstance = new Chart(ctx, {
                        type: 'line',
                        data: {
                            labels: [],
                            datasets: [{
                                label: '残高',
                                data: [],
                                borderColor: 'rgba(54, 162, 235, 1)',
                                backgroundColor: 'rgba(54, 162, 235, 0.1)',
                                fill: true,
                                tension: 0.2,
                                pointRadius: 2
                            }]
                        },
                        options: {
                            responsive: false,
                            maintainAspectRatio: false,
                            plugins: {
                                legend: { display: false },
                                title: { display: false }
                            },
                            scales: {
                                x: { 
                                    title: { display: true, text: this.graphDisplayUnit==='month' ? '年月' : (this.graphDisplayUnit==='year' ? '年' : '日付') },
                                    grid: { display: true }
                                },
                                y: { 
                                    title: { display: true, text: '残高(円)' }, 
                                    beginAtZero: true,
                                    grid: { display: true }
                                }
                            },
                            interaction: {
                                intersect: false,
                                mode: 'index'
                            }
                        }
                    });
                    return;
                }

                // フィルタリングされた各口座の残高を日付別に合計
                // 新しいAPIによりクレジットカード項目が適切にフィルタリング済み
                const dailyTotalBalances = {};
                balanceData.dates.forEach((date, index) => {
                    let totalBalance = 0;
                    balanceData.accounts.forEach(account => {
                        if (balanceData.balances[account] && balanceData.balances[account][index] !== undefined) {
                            totalBalance += balanceData.balances[account][index] || 0;
                        }
                    });
                    dailyTotalBalances[date] = totalBalance;
                });

                // データを表示単位（日/月/年）で集計
                const grouped = {};
                Object.entries(dailyTotalBalances).forEach(([dateStr, balance]) => {
                    const d = new Date(dateStr);
                    let key;
                    if (this.graphDisplayUnit === 'month') {
                        key = `${d.getFullYear()}-${('0'+(d.getMonth()+1)).slice(-2)}`;
                    } else if (this.graphDisplayUnit === 'year') {
                        key = `${d.getFullYear()}`;
                    } else {
                        key = `${d.getFullYear()}-${('0'+(d.getMonth()+1)).slice(-2)}-${('0'+d.getDate()).slice(-2)}`;
                    }
                    // 同じキーの場合は最新バランスを上書き
                    grouped[key] = balance;
                });

                const labels = Object.keys(grouped);
                const data = labels.map(label => grouped[label]);
                
                const ctx = canvas.getContext('2d');
                this._balanceChartInstance = new Chart(ctx, {
                    type: 'line',
                    data: {
                        labels: labels,
                        datasets: [{
                            label: '残高',
                            data: data,
                            borderColor: 'rgba(54, 162, 235, 1)',
                            backgroundColor: 'rgba(54, 162, 235, 0.1)',
                            fill: true,
                            tension: 0.2,
                            pointRadius: 2
                        }]
                    },
                    options: {
                        responsive: false,
                        maintainAspectRatio: false,
                        plugins: {
                            legend: { display: false },
                            title: { display: false }
                        },
                        scales: {
                            x: { 
                                title: { display: true, text: this.graphDisplayUnit==='month' ? '年月' : (this.graphDisplayUnit==='year' ? '年' : '日付') },
                                grid: { display: true }
                            },
                            y: { 
                                title: { display: true, text: '残高(円)' }, 
                                beginAtZero: true,
                                grid: { display: true }
                            }
                        },
                        interaction: {
                            intersect: false,
                            mode: 'index'
                        }
                    }
                });
                
            } catch (error) {
                this.logMessage('error', `残高履歴グラフの描画でエラー: ${error.message}`, 'balance_chart');
                // エラーの場合は空のグラフを表示
                const ctx = canvas.getContext('2d');
                this._balanceChartInstance = new Chart(ctx, {
                    type: 'line',
                    data: {
                        labels: [],
                        datasets: [{
                            label: '残高',
                            data: [],
                            borderColor: 'rgba(54, 162, 235, 1)',
                            backgroundColor: 'rgba(54, 162, 235, 0.1)',
                            fill: true,
                            tension: 0.2,
                            pointRadius: 2
                        }]
                    },
                    options: {
                        responsive: false,
                        maintainAspectRatio: false,
                        plugins: {
                            legend: { display: false },
                            title: { display: false }
                        },
                        scales: {
                            x: { 
                                title: { display: true, text: this.graphDisplayUnit==='month' ? '年月' : (this.graphDisplayUnit==='year' ? '年' : '日付') },
                                grid: { display: true }
                            },
                            y: { 
                                title: { display: true, text: '残高(円)' }, 
                                beginAtZero: true,
                                grid: { display: true }
                            }
                        },
                        interaction: {
                            intersect: false,
                            mode: 'index'
                        }
                    }
                });
            }
        },

        // CSVインポート関連メソッド
        showImportCSVModalMethod() {
            this.logMessage('info', 'CSVインポートモーダルを表示', 'csv_import');
            this.showImportCSVModal = true;
            this.csvFile = null;
            this.csvImportError = null;
            this.csvImportSuccess = null;
            this.csvImporting = false;
            this.csvImportMode = 'append';
            
            // ハンバーガーメニューを閉じる
            this.showMenu = false;
        },

        hideImportCSVModal() {
            this.logMessage('info', 'CSVインポートモーダルを非表示', 'csv_import');
            this.showImportCSVModal = false;
            this.csvFile = null;
            this.csvImportError = null;
            this.csvImportSuccess = null;
            this.csvImporting = false;
            
            // ファイル入力をリセット
            if (this.$refs.csvFileInput) {
                this.$refs.csvFileInput.value = '';
            }
        },

        onCSVFileSelected(event) {
            const file = event.target.files[0];
            if (file) {
                if (!file.name.toLowerCase().endsWith('.csv')) {
                    this.csvImportError = 'CSVファイルを選択してください';
                    this.csvFile = null;
                } else {
                    this.csvFile = file;
                    this.csvImportError = null;
                    this.csvImportSuccess = null;
                    this.logMessage('info', `CSVファイルを選択: ${file.name}`, 'csv_import');
                }
            } else {
                this.csvFile = null;
                this.csvImportError = null;
                this.csvImportSuccess = null;
            }
        },

        async importCSVFile() {
            if (!this.csvFile) {
                this.csvImportError = 'CSVファイルを選択してください';
                return;
            }

            this.csvImporting = true;
            this.csvImportError = null;
            this.csvImportSuccess = null;
            
            this.logMessage('info', `CSVインポートを開始: モード=${this.csvImportMode}, ファイル=${this.csvFile.name}`, 'csv_import');

            try {
                const formData = new FormData();
                formData.append('file', this.csvFile);
                formData.append('mode', this.csvImportMode);

                const response = await fetch('/api/import_csv', {
                    method: 'POST',
                    body: formData
                });

                const result = await response.json();

                if (response.ok) {
                    this.csvImportSuccess = result.message;
                    this.logMessage('info', `CSVインポート成功: ${result.imported_count}件インポート`, 'csv_import');
                    
                    // データを再読み込み
                    await this.loadTransactions();
                    await this.loadFundItems();
                    await this.loadItemNames();
                    
                    // モーダルを3秒後に閉じる
                    setTimeout(() => {
                        if (this.showImportCSVModal) {
                            this.hideImportCSVModal();
                        }
                    }, 3000);
                } else {
                    this.csvImportError = result.error || 'インポートに失敗しました';
                    this.logMessage('error', `CSVインポート失敗: ${this.csvImportError}`, 'csv_import');
                }
            } catch (error) {
                this.csvImportError = `ネットワークエラー: ${error.message}`;
                this.logMessage('error', `CSVインポートエラー: ${error.message}`, 'csv_import');
            }

            this.csvImporting = false;
        },

        // クレジットカード設定関連メソッド
        toggleCreditCardDropdown() {
            this.showCreditCardDropdown = !this.showCreditCardDropdown;
            this.logMessage('debug', `クレジットカードドロップダウンを${this.showCreditCardDropdown ? '表示' : '非表示'}`, 'credit_card');
        },

        toggleCreditCardItem(item) {
            if (this.selectedCreditCardItems.includes(item)) {
                this.selectedCreditCardItems = this.selectedCreditCardItems.filter(i => i !== item);
                this.logMessage('debug', `クレジットカード項目を解除: ${item}`, 'credit_card');
            } else {
                this.selectedCreditCardItems.push(item);
                this.logMessage('debug', `クレジットカード項目を選択: ${item}`, 'credit_card');
            }
        },

        getCreditCardDisplayText() {
            if (this.selectedCreditCardItems.length === 0) {
                return '選択されていません';
            } else if (this.selectedCreditCardItems.length === 1) {
                return this.selectedCreditCardItems[0];
            } else {
                return `${this.selectedCreditCardItems.length}項目選択済み`;
            }
        },

        async loadCreditCardSettings() {
            try {
                this.logMessage('debug', 'クレジットカード設定を読み込み中', 'credit_card');
                
                const response = await fetch('/api/credit_card_settings', {
                    method: 'GET',
                    headers: {
                        'Content-Type': 'application/json'
                    }
                });

                if (response.ok) {
                    const creditCardItems = await response.json();
                    this.selectedCreditCardItems = creditCardItems;
                    this.logMessage('info', `クレジットカード設定を読み込み: ${creditCardItems.length}項目`, 'credit_card');
                } else {
                    const error = await response.json();
                    this.logMessage('error', `クレジットカード設定読み込み失敗: ${error.error}`, 'credit_card');
                }
            } catch (error) {
                this.logMessage('error', `クレジットカード設定読み込みエラー: ${error.message}`, 'credit_card');
            }
        },

        async saveCreditCardSettings() {
            try {
                this.creditCardSettingsMessage = null;
                this.logMessage('info', `クレジットカード設定を保存中: ${this.selectedCreditCardItems.length}項目`, 'credit_card');

                const response = await fetch('/api/credit_card_settings', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify({
                        credit_card_items: this.selectedCreditCardItems
                    })
                });

                const result = await response.json();

                if (response.ok) {
                    this.creditCardSettingsMessage = result.message;
                    this.logMessage('info', 'クレジットカード設定保存成功', 'credit_card');
                    
                    // 3秒後にメッセージを消す
                    setTimeout(() => {
                        this.creditCardSettingsMessage = null;
                    }, 3000);
                } else {
                    this.creditCardSettingsMessage = result.error || '設定の保存に失敗しました。';
                    this.logMessage('error', `クレジットカード設定保存失敗: ${result.error}`, 'credit_card');
                }
            } catch (error) {
                this.creditCardSettingsMessage = `ネットワークエラー: ${error.message}`;
                this.logMessage('error', `クレジットカード設定保存エラー: ${error.message}`, 'credit_card');
            }
        },

        async resetCreditCardSettings() {
            try {
                this.selectedCreditCardItems = [];
                await this.saveCreditCardSettings();
                this.logMessage('info', 'クレジットカード設定をクリア', 'credit_card');
            } catch (error) {
                this.logMessage('error', `クレジットカード設定クリアエラー: ${error.message}`, 'credit_card');
            }
        },

        openCreditCardSettings() {
            this.showCreditCardModal = true;
            this.creditCardSettingsMessage = null;
            this.showCreditCardDropdown = false;
            this.showMenu = false; // メニューを閉じる
            this.logMessage('info', 'クレジットカード設定モーダルを表示', 'credit_card');
        },

        hideCreditCardSettings() {
            this.showCreditCardModal = false;
            this.showCreditCardDropdown = false;
            this.creditCardSettingsMessage = null;
            this.logMessage('info', 'クレジットカード設定モーダルを非表示', 'credit_card');
        }
    },
    mounted() {
        // ドロップダウンの外側をクリックした時に閉じる
        document.addEventListener('click', this.handleClickOutside);
        // ウィンドウリサイズ時にグラフを再描画
        window.addEventListener('resize', this.handleWindowResize);
        // アプリ起動時にAPIからデータを読み込む
        this.loadFundItems().then(() => {
            // 資金項目データ読み込み完了後に取引データとクレジットカード設定を読み込む
            this.loadTransactions();
            this.loadCreditCardSettings();
            this.logMessage('info', 'アプリケーションの初期化が完了しました', 'app');
        });
        // 初期状態では項目名は空（資金項目選択時に取得）
    },
    beforeUnmount() {
        // イベントリスナーをクリーンアップ
        document.removeEventListener('click', this.handleClickOutside);
        window.removeEventListener('resize', this.handleWindowResize);
    },
    created() {
        // Vue app created
    }
}).mount('#app');
