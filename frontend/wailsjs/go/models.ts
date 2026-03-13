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
	export class LinkedTransactionResponse {
	    id: number;
	    fundItem: string;
	    date: string;
	    item: string;
	    type: string;
	    amount: number;
	    memo: string;
	
	    static createFrom(source: any = {}) {
	        return new LinkedTransactionResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.fundItem = source["fundItem"];
	        this.date = source["date"];
	        this.item = source["item"];
	        this.type = source["type"];
	        this.amount = source["amount"];
	        this.memo = source["memo"];
	    }
	}
	export class Tag {
	    id: number;
	    name: string;
	    parent_id?: number;
	    level: number;
	    children?: Tag[];
	
	    static createFrom(source: any = {}) {
	        return new Tag(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.parent_id = source["parent_id"];
	        this.level = source["level"];
	        this.children = this.convertValues(source["children"], Tag);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TagSummary {
	    tag_id: number;
	    tag_name: string;
	    amount: number;
	    count: number;
	    ratio: number;
	    children?: TagSummary[];
	
	    static createFrom(source: any = {}) {
	        return new TagSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tag_id = source["tag_id"];
	        this.tag_name = source["tag_name"];
	        this.amount = source["amount"];
	        this.count = source["count"];
	        this.ratio = source["ratio"];
	        this.children = this.convertValues(source["children"], TagSummary);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TransactionImageRequest {
	    filename: string;
	    data: string;
	    mime_type: string;
	
	    static createFrom(source: any = {}) {
	        return new TransactionImageRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filename = source["filename"];
	        this.data = source["data"];
	        this.mime_type = source["mime_type"];
	    }
	}
	export class TransactionImageResponse {
	    id: number;
	    filename: string;
	    mime_type: string;
	    created_at: string;
	    data_url?: string;
	
	    static createFrom(source: any = {}) {
	        return new TransactionImageResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.filename = source["filename"];
	        this.mime_type = source["mime_type"];
	        this.created_at = source["created_at"];
	        this.data_url = source["data_url"];
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
	    images?: TransactionImageRequest[];
	    tags?: number[];
	
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
	        this.images = this.convertValues(source["images"], TransactionImageRequest);
	        this.tags = source["tags"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
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
	    images?: TransactionImageResponse[];
	    tags?: Tag[];
	
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
	        this.images = this.convertValues(source["images"], TransactionImageResponse);
	        this.tags = this.convertValues(source["tags"], Tag);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

