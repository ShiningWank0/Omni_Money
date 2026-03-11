# Legacy UI Architecture - Complete Exploration Report

This document contains the comprehensive exploration of `/Users/taku/Desktop/Omni_Money/legacy_reference/` directory structure, UI design, and implementation details.

---

## EXECUTIVE SUMMARY

The legacy UI is a **Vue.js 3 + Flask** financial management application with:

- **Frontend:** Vue 3 (Options API), Chart.js, Material Icons
- **Backend:** Flask with SQLAlchemy ORM
- **Styling:** Custom CSS with glass-morphism design
- **Authentication:** Session-based with IP rate limiting
- **Data:** SQLite/PostgreSQL with transaction tracking
- **Charts:** Three types (Line, Pie, Bar) using Chart.js
- **Features:** Multi-select filters, CSV import/export, credit card tracking

---

## 1. HTML TEMPLATE STRUCTURE

### Main Application (index.html - 495 lines)

**Layout:** Two-card system
1. **Header Card** - Navigation, search, fund selector
2. **Content Card** - Balance display + transaction table

**Components:**

#### A. Navigation Area
- **Hamburger Menu** (mobile)
  - Material Icons: "menu"
  - Toggles side drawer
  - Rotates 90° when open

- **Fund Item Selector** (shared across all views)
  - Dropdown with multi-select checkboxes
  - Chevron animation (▶ ↔ ▼)
  - "すべて" (All) option
  - Text display: "すべて" / "N項目選択中" / Single name
  - State: `selectedFundItems[]` (affects entire app)

- **Add Button** (Circular, +)
  - Mobile version: top-right in header-add-btn
  - Desktop version: right side of header-search
  - Opens transaction modal

#### B. Search Bar
- Placeholder: "資金使用項目に対する検索が可能"
- Search icon: 🔍
- Connected to `searchQuery` data property
- Debounced input handling

#### C. Balance Display
- Label: "現在の残高"
- Amount: Large text (3.2rem), formatted currency
- Color: #333 (primary text)
- Uses `formatCurrency()` function

#### D. Transaction Table
- **Sticky Headers** (top: 0, z-index: 5)
- **Columns:** Date | Fund Item | Item | Amount | Balance
- **Fund Item Column:** Conditional (shows if 2+ items selected)
- **Clickable Rows:** Opens edit modal on click
- **Striped Rows:** Alternating white/#f8f9fa
- **Color-coded Amounts:**
  - Income (#e6ffed bg, #155724 text)
  - Expense (#ffe6e6 bg, #721c24 text)
- **Date Sorting:** Click header to toggle ASC/DESC with ▲/▼ indicator

#### E. Side Menu (Drawer)
- Overlay: `side-menu-overlay` (semi-transparent)
- Menu: 260px width, white, slide-in animation
- Options:
  1. CSVバックアップ (backup)
  2. CSVインポート (import)
  3. ログファイルダウンロード (download log)
  4. 残高推移グラフ表示 (balance chart)
  5. 収支比率グラフ (ratio chart)
  6. 項目別収支グラフ (itemized chart)
  7. クレジットカード設定 (credit card settings)
  8. ログアウト (logout - gradient red, margin-top: auto)

### Modal Dialogs

#### 1. Transaction Add/Edit Modal (transaction-modal)
- **Size:** 95% width, max 600px
- **Height:** Full viewport - 4rem (scrollable form)
- **Title:** "新しい取引を追加" or "取引を編集"
- **Form Fields:**
  - Date (date input, required)
  - Time (time input, optional)
  - Fund Item (text + dropdown, required)
  - Type (radio: 収入/支出, required)
  - Item (text with datalist, required)
  - Amount (numeric, required, color-coded)
- **Buttons:** Delete (red, left) | Cancel | OK
- **Special:** "新しい資金項目/項目が作成されます" notice

#### 2. Balance Chart Modal (残高推移グラフ)
- **Size:** 99.5vw, 98vh (full screen)
- **Content:**
  - Title: "残高推移グラフ"
  - Filters:
    - Fund items multi-select dropdown
    - Display unit selector (day/month/year)
  - Canvas: `#balanceChart`
  - Close button

#### 3. Ratio Chart Modal (収支比率グラフ)
- **Size:** 99.5vw, 98vh
- **Content:**
  - Title: "収支比率グラフ"
  - Fund items filter (multi-select)
  - Period navigation: ◀ [Dropdown] ▶
  - Period display text (large, bold)
  - Canvas: `#ratioChart`

#### 4. Itemized Chart Modal (項目別収支グラフ)
- **Size:** 99.5vw, 98vh
- **Content:**
  - Two charts side-by-side
  - Income chart (left): `#incomeItemChart`
  - Expense chart (right): `#expenseItemChart`
  - Same filters and navigation as ratio

#### 5. CSV Import Modal (csv-import-modal)
- File input with accept=".csv"
- Mode selection: append / replace (radio buttons)
- CSV format info box (background: #f8f8f8)
- Progress bar (animated)
- Status messages (error/success colors)
- Import/Cancel buttons

#### 6. Credit Card Settings Modal
- Information box (background: #e3f2fd, blue left border)
- Multi-select dropdown for credit card items
- Save button (green, #4CAF50)
- Reset button (red, #f44336)
- Status message display

### Login Page (login.html - 372 lines)

**Design:** Glass-morphism card on gradient background

- **Background:** `linear-gradient(135deg, #667eea 0%, #764ba2 100%)`
- **Card:**
  - Background: `rgba(255, 255, 255, 0.9)`
  - Blur: 10px
  - Border-radius: 20px
  - Shadow: 0 10px 30px
  - Animation: slideUp 0.6s

- **Title:** "Server Money"
  - Gradient text: Same as background
  - Size: 2rem
  - Weight: 700

- **Form:**
  - Username input
  - Password input
  - Styling: Border color #667eea on focus
  - Login button: Full gradient background
  - Error message: Red background (#ff453a), hidden by default
  - Attempts warning: Orange background (#ff9500)
  - Loading spinner: Rotating circle animation

---

## 2. CSS STYLING DETAILS

### Global Styling (1469 lines)

#### Design System

**Color Palette:**
```css
Primary Gradient: linear-gradient(135deg, #667eea 0%, #764ba2 100%)
Primary Blue: #667eea
Secondary Purple: #764ba2

Text Colors:
  - Primary: #333
  - Secondary: #666
  - Income: #155724 (dark green)
  - Expense: #721c24 (dark red)

Background Colors:
  - Card: rgba(255, 255, 255, 0.9)
  - Income Cell: #e6ffed
  - Expense Cell: #ffe6e6
  - Striped Row: #f8f9fa
  - Modal Overlay: rgba(0, 0, 0, 0.5)
  - Form Error: rgba(255, 69, 58, 0.1)
  - Form Success: rgba(212, 237, 218, 0.9)
  - Credit Card Info: #e3f2fd

Borders:
  - Default: #ddd
  - Table: #dee2e6
  - Focus: #667eea
```

#### Layout Architecture

**Body:**
- Height: 100vh (full height, no scroll)
- Gradient background
- Flex centering
- Padding: 1rem (top/bottom)

**#app Container:**
- Flexbox column
- Width: 95%, max: 1800px
- Height: calc(100vh - 2rem)
- Overflow: hidden (no scroll)
- Display flex, flex-direction column

**Card (.card):**
- Glass-morphism: 
  - Background: rgba(255, 255, 255, 0.9)
  - backdrop-filter: blur(10px)
  - Border-radius: 20px
  - Shadow: 0 10px 30px rgba(0, 0, 0, 0.2)
- Padding: 1.5rem
- Margin-bottom: 1rem

**Header (.header):**
- Z-index: 20
- Flex-shrink: 0 (doesn't compress)
- Desktop: flex-row, gap: 2rem
- Mobile: flex-column, gap: 0.8rem
- Responsive: Changes at 769px breakpoint

**Content-Card (.content-card):**
- Flex: 1 (fills remaining space)
- Overflow: hidden
- Z-index: 10

**Balance Section (.balance-section):**
- Text-align: center
- Border-bottom: 1px solid #eee
- Flex-shrink: 0
- Padding-bottom: 1rem
- Margin-bottom: 1rem
- Font-size (amount): 3.2rem

**Transaction Section (.transaction-section):**
- Flex: 1
- Overflow-y: auto (vertical scroll)
- Min-height: 0 (flexbox sizing trick)

#### Table Styling

```css
.transaction-table {
  width: 100%;
  border-collapse: collapse;
}

.transaction-table thead th {
  position: sticky;
  top: 0;
  background-color: rgba(255, 255, 255, 0.95);
  padding: 1rem;
  border-bottom: 2px solid #dee2e6;
  z-index: 5;
  cursor: pointer; /* sortable */
}

.transaction-table td {
  padding: 1rem;
  border-bottom: 1px solid #dee2e6;
}

.transaction-table tbody tr:nth-child(even) {
  background-color: #f8f9fa;
}

.transaction-table tbody tr:nth-child(odd) {
  background-color: #ffffff;
}

.transaction-table tbody tr {
  cursor: pointer; /* clickable for edit */
}

.income-cell {
  background-color: #e6ffed !important;
  color: #155724;
}

.expense-cell {
  background-color: #ffe6e6 !important;
  color: #721c24;
}
```

#### Form & Input Styling

```css
.form-row input, .form-row select {
  width: 100%;
  padding: 0.75rem;
  border: 1px solid #ddd;
  border-radius: 8px;
  font-size: 1rem;
  transition: all 0.3s ease;
}

.form-row input:focus, .form-row select:focus {
  outline: none;
  border-color: #667eea;
  box-shadow: 0 0 0 2px rgba(102, 126, 234, 0.2);
}

.amount-input-income {
  border-color: #28a745 !important;
  background-color: #f8fff9 !important;
}

.amount-input-income:focus {
  box-shadow: 0 0 0 2px rgba(40, 167, 69, 0.2) !important;
}

.amount-input-expense {
  border-color: #dc3545 !important;
  background-color: #fff8f8 !important;
}

.amount-input-expense:focus {
  box-shadow: 0 0 0 2px rgba(220, 53, 69, 0.2) !important;
}

.radio-group {
  display: flex;
  gap: 1rem;
}

.radio-group label {
  display: flex;
  align-items: center;
  cursor: pointer;
}

.radio-group input[type="radio"] {
  width: auto;
  margin-right: 0.5rem;
}
```

#### Button Styling

```css
.add-btn {
  width: 40px;
  height: 40px;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.9);
  color: #667eea;
  border: 1px solid #ddd;
  font-size: 1.2rem;
  cursor: pointer;
  transition: all 0.2s ease;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.add-btn:hover {
  background: rgba(255, 255, 255, 1);
  border-color: #667eea;
  box-shadow: 0 4px 8px rgba(0, 0, 0, 0.15);
  transform: translateY(-1px);
}

.add-btn:active {
  transform: translateY(0);
  box-shadow: 0 2px 4px rgba(0, 0, 0, 0.1);
}

.ok-btn {
  background: #667eea;
  color: white;
  padding: 0.75rem 1.5rem;
  border: none;
  border-radius: 8px;
  font-size: 1rem;
  cursor: pointer;
  transition: background-color 0.2s;
}

.ok-btn:hover {
  background: #5a6fd8;
}

.cancel-btn {
  background: #f5f5f5;
  color: #333;
  padding: 0.75rem 1.5rem;
  border: none;
  border-radius: 8px;
}

.cancel-btn:hover {
  background: #e0e0e0;
}

.delete-btn {
  background: #ff4d4f;
  color: white;
  padding: 0.75rem 1.5rem;
  border: none;
  border-radius: 8px;
  cursor: pointer;
  transition: background-color 0.2s;
}

.delete-btn:hover {
  background: #d9363e;
}

.menu-btn {
  width: 100%;
  padding: 0.75rem 1rem;
  border: none;
  border-radius: 8px;
  font-size: 1.1rem;
  background: #f5f5f5;
  color: #333;
  cursor: pointer;
  transition: background 0.2s;
}

.menu-btn:hover {
  background: #e0e0e0;
}

.logout-btn {
  background: linear-gradient(135deg, #f44336 0%, #d32f2f 100%) !important;
  color: white !important;
  margin-top: auto;
}

.logout-btn:hover {
  background: linear-gradient(135deg, #d32f2f 0%, #c62828 100%) !important;
  transform: translateY(-1px);
  box-shadow: 0 4px 8px rgba(244, 67, 54, 0.3);
}
```

#### Modal Styling

```css
.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  justify-content: center;
  align-items: center;
  z-index: 1000;
}

.modal-content {
  background: white;
  border-radius: 15px;
  padding: 1.5rem;
  width: 95%;
  max-width: 600px;
  max-height: calc(100vh - 4rem);
  overflow: hidden;
  display: flex;
  flex-direction: column;
  box-sizing: border-box;
}

.modal-content.transaction-modal {
  height: calc(100vh - 4rem);
  max-height: calc(100vh - 4rem);
}

.modal-content.transaction-modal h3 {
  flex-shrink: 0;
  margin: 0 0 1rem 0;
  text-align: center;
}

.modal-content.transaction-modal form {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.modal-content.transaction-modal .form-container {
  flex: 1;
  overflow-y: auto;
  padding-right: 0.5rem;
}

.graph-modal-xlarge {
  max-width: 99.5vw;
  width: 100vw;
  height: 98vh;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: flex-start;
  padding: 8px 0 4px 0;
}

.graph-scroll-wrapper {
  width: 100%;
  max-width: calc(99.5vw - 15px);
  max-height: calc(98vh - 70px);
  overflow: auto;
  background: #f8fafc;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
}
```

#### Dropdown Styling

```css
.account-dropdown {
  position: absolute;
  top: 100%;
  left: 0;
  background-color: white;
  border: 1px solid #ddd;
  border-radius: 4px;
  box-shadow: 0 2px 8px rgba(0,0,0,0.15);
  z-index: 9999;
  min-width: 180px;
  max-height: 200px;
  overflow-y: auto;
}

.account-dropdown li {
  padding: 0.75rem 1rem;
  cursor: pointer;
  font-size: 1rem;
  color: #333;
}

.account-dropdown li:hover {
  background-color: #f5f5f5;
}

.funditem-dropdown {
  position: absolute;
  top: 100%;
  left: 0;
  right: 0;
  background: white;
  border: 2px solid #ddd;
  border-radius: 0 0 8px 8px;
  max-height: 200px;
  overflow-y: auto;
  z-index: 1000;
}

.funditem-dropdown li {
  padding: 0.75rem;
  cursor: pointer;
  transition: background-color 0.2s ease;
  border-bottom: 1px solid #f0f0f0;
}

.funditem-dropdown li:hover {
  background-color: #f8f9fa;
}

.funditem-dropdown li.selected {
  background-color: #667eea;
  color: white;
}

.multi-select-dropdown {
  position: absolute;
  top: 100%;
  left: 0;
  right: 0;
  background: white;
  border: 1px solid #ccc;
  border-radius: 4px;
  z-index: 1000;
  margin-top: 2px;
}
```

#### Checkbox Styling

```css
.fund-item-checkbox {
  display: flex;
  align-items: center;
  padding: 6px 12px;
  cursor: pointer;
  transition: background-color 0.2s;
  font-size: 0.9rem;
}

.fund-item-checkbox:hover {
  background-color: #f0f0f0;
}

.fund-item-checkbox input[type="checkbox"] {
  display: none;
}

.checkmark {
  position: relative;
  width: 16px;
  height: 16px;
  margin-right: 8px;
  border: 2px solid #ddd;
  border-radius: 3px;
  background-color: white;
  transition: all 0.2s;
}

.fund-item-checkbox input[type="checkbox"]:checked + .checkmark {
  background-color: #007bff;
  border-color: #007bff;
}

.fund-item-checkbox input[type="checkbox"]:checked + .checkmark::after {
  content: '✓';
  position: absolute;
  top: -1px;
  left: 1px;
  color: white;
  font-size: 12px;
  font-weight: bold;
}
```

#### Animations

```css
@keyframes slideUp {
  from {
    opacity: 0;
    transform: translateY(30px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

@keyframes slideInLeft {
  from { transform: translateX(-100%); }
  to { transform: translateX(0); }
}

@keyframes spin {
  0% { transform: rotate(0deg); }
  100% { transform: rotate(360deg); }
}

@keyframes chevron-down-anim {
  0% {
    transform: rotate(0deg) scale(1);
    opacity: 0.7;
  }
  60% {
    transform: rotate(60deg) scale(1.1);
    opacity: 1;
  }
  100% {
    transform: rotate(0deg) scale(1);
    opacity: 1;
  }
}

@keyframes chevron-up-anim {
  0% {
    transform: rotate(0deg) scale(1);
    opacity: 1;
  }
  100% {
    transform: rotate(-60deg) scale(0.95);
    opacity: 0.7;
  }
}

@keyframes progress-animation {
  0% { transform: translateX(-100%); }
  100% { transform: translateX(100%); }
}
```

#### Responsive Design

**Tablet (481-768px):**
- Body padding reduced
- Card padding reduced
- Header stacks vertically
- Table font size: 0.8rem
- Table cell padding: 0.5rem 0.3rem
- Modal width: 98%

**Mobile (<480px):**
- Padding: 0.25rem
- Table font size: 0.75rem
- Modal: 100% width
- All spacing 50% reduced
- Hamburger menu becomes primary navigation

---

## 3. JAVASCRIPT IMPLEMENTATION

### Framework: Vue.js 3 (Options API, CDN: vue@3)

### Data Properties (main.js - 1866 lines)

#### Transaction Management
```javascript
transactions: []              // All transactions from DB
newTransaction: {
  fundItem: '',
  date: '',
  time: '',
  item: '',
  type: 'expense',
  amount: ''
}
editTransactionId: null       // ID of being-edited transaction
isEditMode: false             // Boolean: edit vs add
```

#### UI State
```javascript
showAddTransactionModal: false
showGraph: false              // Balance chart modal
showRatioModal: false         // Ratio chart modal
showItemizedModal: false      // Itemized chart modal
showMenu: false               // Side drawer
showAccountDropdown: false    // Fund selector dropdown
showFundItemDropdown: false   // Fund item input dropdown
showGraphFundItemDropdown: false
showRatioFundItemDropdown: false
showItemizedFundItemDropdown: false
showCreditCardDropdown: false
```

#### Data Lists
```javascript
fundItemNames: []            // Distinct fund items
itemNames: []                // Distinct transaction items
selectedFundItems: []        // Currently selected (multi-select)
selectedCreditCardItems: []  // Credit card fund items
```

#### Chart State
```javascript
ratioChartInstance: null
incomeItemChartInstance: null
expenseItemChartInstance: null
balanceChartInstance: null
```

#### Filter Settings
```javascript
dateSortOrder: 'desc'        // 'asc' or 'desc'
searchQuery: ''              // Search term
graphDisplayUnit: 'day'      // 'day', 'month', 'year'
ratioDisplayUnit: 'all'      // 'all', 'day', 'month', 'year'
itemizedDisplayUnit: 'all'   // 'all', 'day', 'month', 'year'
```

#### CSV & Credit Card
```javascript
showImportCSVModal: false
csvFile: null
csvImportMode: 'append'
csvImporting: false
csvImportError: null
csvImportSuccess: null
showCreditCardModal: false
creditCardSettingsMessage: null
```

### Computed Properties (Key Ones)

```javascript
filteredTransactions() {
  // Filter by selectedFundItems + searchQuery
  // Returns Transaction[]
}

sortedTransactions() {
  // filteredTransactions sorted by date
  // Direction: dateSortOrder
  // Returns Transaction[]
}

currentBalance() {
  // Sum of filtered transactions
  // income: +amount, expense: -amount
  // Returns number
}

shouldShowFundItemColumn() {
  // Show fund item column if 2+ items selected
  // Returns boolean
}

selectedFundItemDisplay() {
  // Text for dropdown display
  // "すべて" / "N項目選択中" / Single name
  // Returns string
}

actualFundItems() {
  // fundItemNames minus "すべて"
  // Returns string[]
}

ratioDisplayOptions() {
  // [ {value, text}, ... ]
  // Options: all, year, month, day
  // Returns array
}

itemizedDisplayOptions() {
  // Same as ratioDisplayOptions
  // Returns array
}

ratioCurrentPeriodDisplay() {
  // "2024年" or "2024年1月" or "2024年1月15日"
  // Empty string for 'all'
  // Returns string
}

itemizedCurrentPeriodDisplay() {
  // Same as ratioCurrentPeriodDisplay
  // Returns string
}
```

### Methods (Key Ones)

#### Data Loading
```javascript
async loadData() {
  // GET /api/transactions
  // Populates this.transactions
  // Handles errors
}

async loadFundItems() {
  // GET /api/accounts
  // Populates this.fundItemNames
  // Sorts alphabetically
}

async loadItemNames() {
  // GET /api/items
  // Populates this.itemNames
  // Sorts alphabetically
}

async loadCreditCardSettings() {
  // GET /api/credit_card_settings
  // Populates this.selectedCreditCardItems
}
```

#### Transaction Management
```javascript
async addOrUpdateTransaction() {
  // If isEditMode: PUT /api/transactions/<id>
  // Else: POST /api/transactions
  // Request body: account, date, time?, item, type, amount
  // Validates form before submit
  // Recalculates balances
  // Closes modal on success
}

onEditTransaction(transaction) {
  // Populate newTransaction with data
  // Set isEditMode = true
  // Set editTransactionId
  // Show modal
}

async onDeleteTransaction() {
  // DELETE /api/transactions/<id>
  // Recalculates balances
  // Closes modal
}

onAmountInput(event) {
  // Real-time numeric formatting
  // Removes non-digits
  // Formats with commas
}

onAmountKeydown(event) {
  // Allows only: digits, delete, backspace, tab
  // Blocks other keys
}

onAmountPaste(event) {
  // Formats pasted value
  // Removes non-digits
}
```

#### UI Methods
```javascript
showAddModal() {
  // Reset newTransaction
  // Set isEditMode = false
  // Show modal
}

hideAddModal() {
  // Clear form
  // Hide modal
}

toggleMenu() {
  // Toggle showMenu
  // Closes dropdown on open
}

toggleAccountDropdown() {
  // Toggle showAccountDropdown
  // Closes menu
}

toggleFundItem(name) {
  // Add/remove from selectedFundItems
  // Triggers watch → recalculate
}

toggleAllFundItems() {
  // If all selected: clear
  // Else: select all
  // Triggers watch
}

toggleFundItemDropdown() {
  // Toggle showFundItemDropdown
}

selectFundItemInModal(item) {
  // Set newTransaction.fundItem = item
  // Hide dropdown
}

onSearchInput(event) {
  // Debounce search
  // Update searchQuery
  // Filtered transactions update automatically
}

toggleDateSort() {
  // Toggle dateSortOrder
  // Re-render table
}
```

#### Chart Methods
```javascript
async renderBalanceChart() {
  // Destroy old instance
  // Fetch data from /api/balance_history_filtered
  // Filter by selectedFundItems
  // Create Chart.js line chart
  // Store instance in ratioChartInstance
  // Handle canvas sizing for mobile
}

async renderRatioChart() {
  // Destroy old instance
  // Fetch /api/transactions
  // Filter by selectedFundItems + ratioDisplayUnit
  // Calculate income/expense totals
  // Create Chart.js pie chart
  // Display as percentages
}

async renderItemizedCharts() {
  // Destroy old instances
  // Fetch /api/transactions
  // Group by item (income vs expense)
  // Create two horizontal bar charts
  // Position side-by-side with flex layout
  // Handle canvas sizing
}
```

#### Chart Navigation
```javascript
navigateRatioPeriod(direction) {
  // direction: -1 (previous) or +1 (next)
  // Update ratioCurrentDate
  // Recalculate available periods
  // Re-render chart
}

navigateItemizedPeriod(direction) {
  // Same as navigateRatioPeriod but for itemized
}

canNavigateRatioPeriod(direction) {
  // Check if navigation is allowed
  // Returns boolean
}

canNavigateItemizedPeriod(direction) {
  // Same as canNavigateRatioPeriod
}

onRatioDisplayUnitChange() {
  // Update ratioDisplayUnit
  // Reset date to today
  // Trigger chart re-render
}

onItemizedDisplayUnitChange() {
  // Same as onRatioDisplayUnitChange
}

toggleAllGraphFundItems() {
  // Same as toggleAllFundItems
  // Used in balance chart modal
}

toggleGraphFundItem(item) {
  // Same as toggleFundItem
  // For balance chart modal
}
```

#### CSV Operations
```javascript
async backupToCSV() {
  // GET /api/backup_csv
  // Triggers browser download
  // Filename: transactions_backup_TIMESTAMP.csv
}

showImportCSVModalMethod() {
  // Show CSV import modal
}

async importCSVFile() {
  // Create FormData with file + mode
  // POST /api/import_csv
  // Show progress bar
  // Display success/error message
  // Reload data on success
}

onCSVFileSelected(event) {
  // Store file reference
  // Update csvFile property
}

hideImportCSVModal() {
  // Clear form
  // Hide modal
}
```

#### Credit Card Settings
```javascript
openCreditCardSettings() {
  // Load current settings
  // Show modal
}

async saveCreditCardSettings() {
  // POST /api/credit_card_settings
  // Request: {credit_card_items: []}
  // Show success message
  // Reload data if changed
}

async resetCreditCardSettings() {
  // Clear selectedCreditCardItems
  // Update API
  // Show success message
}

toggleCreditCardDropdown() {
  // Toggle dropdown visibility
}

toggleCreditCardItem(item) {
  // Add/remove from selectedCreditCardItems
}

hideCreditCardSettings() {
  // Clear form
  // Hide modal
}

getCreditCardDisplayText() {
  // "すべて" / "N項目選択中" / Item names
  // Returns string
}
```

#### Utility Methods
```javascript
formatCurrency(amount) {
  // 1000000 → "1,000,000 円"
  // Returns formatted string
}

formatAmount(amount, type) {
  // income: "＋1,000,000 円"
  // expense: "－5,000 円"
  // Returns string with sign
}

formatDateTime(dateStr) {
  // "2024-01-15 14:30:00" → "2024-01-15 14:30"
  // Returns formatted string
}

getAmountCellClass(type) {
  // 'income' → 'income-cell'
  // 'expense' → 'expense-cell'
  // Returns CSS class name
}

getAmountInputClass(type) {
  // 'income' → 'amount-input-income'
  // 'expense' → 'amount-input-expense'
  // Returns CSS class
}

isNewFundItem(name) {
  // Check if fundItem doesn't exist in DB
  // Returns boolean
}

isNewItem(name) {
  // Check if item doesn't exist in DB
  // Returns boolean
}

isFundItemSelected(name) {
  // Check if in selectedFundItems
  // Returns boolean
}

logMessage(level, message, component = 'main') {
  // POST /api/log
  // Sends log to backend
}

logout() {
  // POST /api/logout
  // Redirect to login page
}
```

#### Modal Display Logic
```javascript
showGraph = true → renderBalanceChart()
showRatioModal = true → renderRatioChart()
showItemizedModal = true → renderItemizedCharts()
```

### Lifecycle Hooks

```javascript
mounted() {
  // Called when component ready
  // Calls: loadData(), loadFundItems(), loadItemNames()
  //        loadCreditCardSettings()
  // Sets up watchers
}

watch: {
  selectedFundItems() {
    // Recalculate when fund items change
    // Re-render charts
    // Update balance
  }
}
```

### Event Handlers

- Click on table row → `onEditTransaction()`
- Click fund item selector → `toggleAccountDropdown()`
- Click checkbox → `toggleFundItem()`
- Input search box → `onSearchInput()` (debounced)
- Click date header → `toggleDateSort()`
- Amount input → `onAmountInput()` (formatting)
- Modal overlay click → `hideAddModal()` / `hideGraphModal()` etc.

---

## 4. BACKEND ROUTES & API

### Authentication Routes (routes/auth_routes.py - 107 lines)

**GET /login**
- Renders login page
- Redirects to main if already logged in

**POST /api/login**
```javascript
Request: {username, password}
Response: {
  success: true,
  message: "ログインしました",
  redirect: "/"
}
Status: 200

OR

{
  error: "ユーザー名またはパスワードが正しくありません",
  remaining_attempts: 2
}
Status: 401
```
- Validates credentials
- Sets session cookie
- IP-based rate limiting (max 3 attempts/10 min)
- Logs attempt

**POST /api/logout**
- Clears session
- Response: {success, message}

**GET /api/auth_status**
- Returns auth status + user info
- No authentication required

---

### Transaction Routes (routes/api_routes.py - 688 lines)

**GET /api/accounts**
- Returns distinct account names
- Response: `string[]`
- Example: `["銀行口座", "クレジットカード"]`

**GET /api/items**
- Query params: `account` (optional, filter by fund item)
- Response: `string[]` (distinct item names)
- Example: `["給与", "食費", "交通費"]`

**GET /api/transactions**
- Query params: `search`, `account` (both optional)
- Response: `Transaction[]`
```javascript
Transaction {
  id: number,
  account: string,
  date: string (ISO),
  item: string,
  type: "income" | "expense",
  amount: number,
  balance: number | null
}
```

**POST /api/transactions**
```javascript
Request: {
  account: string,
  date: "YYYY-MM-DD",
  time: "HH:MM" (optional),
  item: string,
  type: "income" | "expense",
  amount: number
}

Response: {
  message: "取引が正常に追加されました",
  transaction: Transaction
}
Status: 201

OR

{error: "..."}
Status: 400 | 500
```

**PUT/PATCH /api/transactions/<id>**
- Same request/response as POST
- Updates existing transaction
- Recalculates balances for affected accounts

**DELETE /api/transactions/<id>**
- Response: {message: "取引が削除されました"}
- Status: 200 or 404

---

### Analytics Routes

**GET /api/balance_history**
```javascript
Response: {
  accounts: string[],
  dates: string[] (YYYY-MM-DD),
  balances: {
    "account1": [0, 10000, 20000],
    "account2": [0, 5000, 10000]
  }
}
```
- Used by balance chart
- All accounts, no filtering

**GET /api/balance_history_filtered**
```javascript
Query params: fund_items (multi-value array)
Response: {
  accounts: string[],
  dates: string[],
  balances: {...}
}
```
- Filters out credit card accounts
- Only returns selected fund items
- Used by balance chart with filters

---

### Data Export/Import Routes

**GET /api/backup_csv**
- Downloads CSV file
- Filename: `transactions_backup_TIMESTAMP.csv`
- Columns: `id, account, date, item, type, amount, balance`
- Keeps max 3 backups

**POST /api/import_csv**
```javascript
Request: FormData {
  file: File (CSV),
  mode: "append" | "replace"
}

Response: {
  message: "CSVファイルのインポートが完了しました。10件のトランザクションを追加しました。",
  imported_count: 10,
  mode: "append"
}
Status: 200

OR

{error: "..."}
Status: 400 | 500
```
- Supports UTF-8 and Shift_JIS encoding
- Validates CSV format
- Recalculates balances

**GET /api/download_log**
- Downloads log file
- Filename: `money_tracker_log_TIMESTAMP.log`

**POST /api/log**
```javascript
Request: {
  level: "info" | "debug" | "warning" | "error",
  message: string,
  component: string
}
Response: {status: "logged"}
```
- Frontend logging to backend
- Used for debugging

---

### Credit Card Settings Routes

**GET /api/credit_card_settings**
- Response: `string[]` (fund item names)
- Empty array if not set

**POST /api/credit_card_settings**
```javascript
Request: {
  credit_card_items: string[]
}

Response: {
  message: "クレジットカード設定を保存しました",
  credit_card_items: [...]
}
Status: 200

OR

{error: "..."}
Status: 400 | 500
```
- Validates accounts exist
- Saves to JSON file in instance folder

---

### Main Route (routes/main_routes.py - 17 lines)

**GET /**
- Requires login (decorator: @login_required)
- Renders index.html

---

## 5. DATA FLOW & STATE MANAGEMENT

### Loading Flow
```
mounted()
  ↓
loadData() → GET /api/transactions
loadFundItems() → GET /api/accounts
loadItemNames() → GET /api/items
loadCreditCardSettings() → GET /api/credit_card_settings
  ↓
this.transactions = [...]
this.fundItemNames = [...]
this.itemNames = [...]
this.selectedCreditCardItems = [...]
```

### Adding Transaction
```
User clicks + button
  ↓
showAddModal() resets form, sets isEditMode = false
  ↓
User fills form + clicks OK
  ↓
addOrUpdateTransaction() validates
  ↓
POST /api/transactions {account, date, time, item, type, amount}
  ↓
Backend: Calculate new balance, insert DB
  ↓
Response: {message, transaction}
  ↓
loadData() reload transactions
  ↓
hideAddModal()
  ↓
Table updates automatically via computed property
```

### Filtering Flow
```
User selects fund items via dropdown
  ↓
toggleFundItem(name) updates selectedFundItems[]
  ↓
Watch detects change
  ↓
filteredTransactions computed property recalculates
  ↓
sortedTransactions uses filtered results
  ↓
Table re-renders with new rows
  ↓
currentBalance recalculates
  ↓
Charts trigger re-render if visible
```

### Chart Rendering Flow
```
User clicks chart menu item
  ↓
showGraph/showRatioModal/showItemizedModal = true
  ↓
Computed property returns options
  ↓
Modal displays with filters
  ↓
renderBalanceChart/renderRatioChart/renderItemizedCharts() called
  ↓
Fetch data from /api/balance_history_filtered or /api/transactions
  ↓
Filter by selectedFundItems
  ↓
Group/aggregate data (by date or item)
  ↓
Create Chart.js instance with data
  ↓
Canvas renders visualization
```

---

## 6. KEY DESIGN PATTERNS

### Multi-select Filtering
- Selected items stored in `selectedFundItems[]`
- Affects multiple views (table, charts, balance calc)
- Shared state across all components
- Toggle individual or all at once
- Display text changes based on selection count

### Two-mode Modal
- Same modal for add and edit
- `isEditMode` boolean flag
- Different title based on mode
- Delete button only visible in edit mode
- Form reset on close

### Chart Cleanup & Recreation
- Old Chart.js instance destroyed before new one created
- Prevents memory leaks
- Supports period/filter changes
- Uses `this.$nextTick()` for DOM updates

### Debounced Search
- `searchTimeout` used for debouncing
- Reduces API calls
- Responsive UI feedback

### Computed Properties for Derived Data
- `filteredTransactions` from `transactions` + filters
- `sortedTransactions` from `filteredTransactions` + sort
- `currentBalance` from `sortedTransactions` calculations
- Automatic recalculation on dependency change

### API Response Handling
- All APIs return JSON
- Error responses include `error` field
- Success responses include `message` or data
- Frontend logs errors via `/api/log`

---

## 7. SECURITY FEATURES

### Authentication
- Session-based (cookies)
- Username + password validation
- Backend credential storage via environment variables
- Login page separate from main app

### Rate Limiting
- IP address tracking
- Max 3 failed attempts per 10 minutes
- Returns 429 status code when locked
- Displays remaining attempts to user

### Authorization
- `@login_required` decorator on all protected routes
- Session check on every request
- Logout clears session

### Data Validation
- Frontend: Required field checks
- Backend: `validate_transaction_data()` function
- Transaction amount must be positive integer
- Date format validation (YYYY-MM-DD)

---

## 8. RESPONSIVE DESIGN STRATEGY

### Breakpoints
- **Desktop:** 769px+ (primary design)
- **Tablet:** 481-768px (optimized layout)
- **Mobile:** 0-480px (minimal spacing)

### Layout Changes

| Property | Desktop | Tablet | Mobile |
|----------|---------|--------|--------|
| Header | Flex row | Flex col | Flex col |
| Add btn | Desktop only | Mobile | Mobile |
| Table font | 1rem | 0.8rem | 0.75rem |
| Padding | Full | Reduced | Minimal |
| Modal width | 600px | 98% | 100% |
| Card padding | 1.5rem | 1rem | 0.8rem |

### Flexbox Sizing Tricks
- `flex: 1` on flex children to fill space
- `min-height: 0` to allow flex children to shrink below content size
- `flex-shrink: 0` to prevent header/footer compression

---

## 9. BROWSER COMPATIBILITY

### Modern Browser Features Used
- CSS Grid / Flexbox
- CSS Custom Properties (not used, but could be)
- `backdrop-filter: blur()` (Glass-morphism)
- `position: sticky` (Table headers)
- ES6+ JavaScript (arrow functions, async/await, destructuring)
- Fetch API
- FormData API

### Fallbacks Needed For:
- `backdrop-filter` → Solid background for older browsers
- `position: sticky` → Fixed positioning for older browsers

---

## 10. PERFORMANCE CONSIDERATIONS

### Optimizations
- Debounced search input
- Chart instances reused/destroyed properly
- Sticky table headers (no layout thrashing)
- Computed properties cache results
- Modal overlays: Click outside to close (no extra buttons)

### Potential Issues
- Large transaction lists (1000+) may slow table rendering
- Multiple charts open simultaneously (memory usage)
- CSV import of large files (backend processing)

### Solutions
- Virtual scrolling for large tables
- Pagination for transaction lists
- Chart data aggregation (daily/monthly averages)

---

## 11. DEPLOYMENT FILES

### Key Files
```
/legacy_reference/
├── templates/
│   ├── index.html (495 lines, main app)
│   └── login.html (372 lines, login page)
├── static/
│   ├── css/style.css (1469 lines)
│   └── js/main.js (1866 lines)
├── routes/
│   ├── main_routes.py (17 lines)
│   ├── auth_routes.py (107 lines)
│   └── api_routes.py (688 lines)
├── app.py (Flask app initialization)
├── models.py (SQLAlchemy models)
├── auth.py (Authentication utilities)
├── config.py (Configuration)
└── utils.py (Helper functions)
```

### External Dependencies
- **Frontend:** Vue 3, Chart.js, Material Icons (CDN)
- **Backend:** Flask, SQLAlchemy, Flask-Session
- **Database:** SQLite (default) or PostgreSQL

---

## 12. REPLICATION IN MODERN VUE.JS

### Recommended Stack
- **Vue 3** (Composition API or Options API)
- **TypeScript** (type safety)
- **Vite** (build tool)
- **Tailwind CSS** (replace CSS file)
- **Chart.js** (same library)
- **Axios** (HTTP client, instead of fetch)
- **Pinia** (state management for complex apps)

### Component Structure
```
App.vue
├── LoginView.vue
└── MainLayout.vue
    ├── Header.vue
    │   ├── FundItemSelector.vue
    │   └── SearchBar.vue
    ├── BalanceDisplay.vue
    ├── TransactionTable.vue
    │   └── TransactionRow.vue
    ├── SideMenu.vue
    ├── AddTransactionModal.vue
    ├── BalanceChartModal.vue
    ├── RatioChartModal.vue
    ├── ItemizedChartModal.vue
    ├── CSVImportModal.vue
    └── CreditCardSettingsModal.vue
```

### State Management with Pinia
```javascript
// stores/transaction.ts
export const useTransactionStore = defineStore('transaction', {
  state: () => ({
    transactions: [],
    fundItems: [],
    selectedFundItems: [],
    searchQuery: '',
    // ...
  }),
  getters: {
    filteredTransactions: (state) => { ... },
    currentBalance: (state) => { ... },
  },
  actions: {
    async loadTransactions() { ... },
    async addTransaction(data) { ... },
    toggleFundItem(name) { ... },
  }
})
```

---

## CONCLUSION

The legacy UI is a well-structured, feature-rich financial management application with:

1. **Clean Separation of Concerns** - HTML templates, CSS styling, JavaScript logic
2. **Responsive Design** - Works on desktop, tablet, mobile
3. **Modern UI Patterns** - Glass-morphism, multi-select filters, modal dialogs
4. **Robust Backend** - RESTful API, proper validation, rate limiting
5. **User-Friendly Features** - Real-time search, CSV import/export, charts
6. **Security** - Authentication, authorization, rate limiting

When replicating in modern Vue.js, maintain these principles while leveraging Vue 3 composition, TypeScript, and modern build tools for better maintainability and performance.

