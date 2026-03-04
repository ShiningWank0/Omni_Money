export namespace models {
	
	export class BalanceHistoryResponse {
	    accounts: string[];
	    dates: string[];
	    balances: Record<string, Array<number>>;
	
	    static createFrom(source: any = {}) {
	        return new BalanceHistoryResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accounts = source["accounts"];
	        this.dates = source["dates"];
	        this.balances = source["balances"];
	    }
	}
	export class TransactionRequest {
	    account: string;
	    date: string;
	    time: string;
	    item: string;
	    type: string;
	    amount: number;
	    memo: string;
	
	    static createFrom(source: any = {}) {
	        return new TransactionRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.account = source["account"];
	        this.date = source["date"];
	        this.time = source["time"];
	        this.item = source["item"];
	        this.type = source["type"];
	        this.amount = source["amount"];
	        this.memo = source["memo"];
	    }
	}
	export class TransactionResponse {
	    id: number;
	    fundItem: string;
	    account: string;
	    date: string;
	    item: string;
	    type: string;
	    amount: number;
	    balance: number;
	    memo: string;
	
	    static createFrom(source: any = {}) {
	        return new TransactionResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.fundItem = source["fundItem"];
	        this.account = source["account"];
	        this.date = source["date"];
	        this.item = source["item"];
	        this.type = source["type"];
	        this.amount = source["amount"];
	        this.balance = source["balance"];
	        this.memo = source["memo"];
	    }
	}

}

