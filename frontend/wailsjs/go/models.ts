export namespace core {

	export class SubscriptionCalendarEntry {
	    date: string;
	    rule_id: string;
	    item: string;
	    account: string;
	    currency: string;
	    original_amount: string;
	    transaction?: models.TransactionResponse;

	    static createFrom(source: any = {}) {
	        return new SubscriptionCalendarEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.date = source["date"];
	        this.rule_id = source["rule_id"];
	        this.item = source["item"];
	        this.account = source["account"];
	        this.currency = source["currency"];
	        this.original_amount = source["original_amount"];
	        this.transaction = this.convertValues(source["transaction"], models.TransactionResponse);
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
	export class SubscriptionCandidate {
	    fingerprint: string;
	    interval: string;
	    transaction: models.TransactionResponse;
	    count: number;

	    static createFrom(source: any = {}) {
	        return new SubscriptionCandidate(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fingerprint = source["fingerprint"];
	        this.interval = source["interval"];
	        this.transaction = this.convertValues(source["transaction"], models.TransactionResponse);
	        this.count = source["count"];
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
	export class SubscriptionHistory {
	    transactions: models.TransactionResponse[];
	    next_offset: number;

	    static createFrom(source: any = {}) {
	        return new SubscriptionHistory(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.transactions = this.convertValues(source["transactions"], models.TransactionResponse);
	        this.next_offset = source["next_offset"];
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
	export class SubscriptionInput {
	    source_transaction_id: number;
	    revision: number;
	    account: string;
	    item: string;
	    memo: string;
	    currency: string;
	    original_amount: string;
	    interval: string;
	    anchor_day: number;
	    anchor_month: number;
	    timezone: string;
	    next_date: string;
	    enabled: boolean;
	    tags: number[];

	    static createFrom(source: any = {}) {
	        return new SubscriptionInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source_transaction_id = source["source_transaction_id"];
	        this.revision = source["revision"];
	        this.account = source["account"];
	        this.item = source["item"];
	        this.memo = source["memo"];
	        this.currency = source["currency"];
	        this.original_amount = source["original_amount"];
	        this.interval = source["interval"];
	        this.anchor_day = source["anchor_day"];
	        this.anchor_month = source["anchor_month"];
	        this.timezone = source["timezone"];
	        this.next_date = source["next_date"];
	        this.enabled = source["enabled"];
	        this.tags = source["tags"];
	    }
	}
	export class SubscriptionRule {
	    source_transaction_id: number;
	    revision: number;
	    account: string;
	    item: string;
	    memo: string;
	    currency: string;
	    original_amount: string;
	    interval: string;
	    anchor_day: number;
	    anchor_month: number;
	    timezone: string;
	    next_date: string;
	    enabled: boolean;
	    tags: number[];
	    id: string;

	    static createFrom(source: any = {}) {
	        return new SubscriptionRule(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source_transaction_id = source["source_transaction_id"];
	        this.revision = source["revision"];
	        this.account = source["account"];
	        this.item = source["item"];
	        this.memo = source["memo"];
	        this.currency = source["currency"];
	        this.original_amount = source["original_amount"];
	        this.interval = source["interval"];
	        this.anchor_day = source["anchor_day"];
	        this.anchor_month = source["anchor_month"];
	        this.timezone = source["timezone"];
	        this.next_date = source["next_date"];
	        this.enabled = source["enabled"];
	        this.tags = source["tags"];
	        this.id = source["id"];
	    }
	}
	export class SubscriptionSync {
	    created: number;
	    has_more: boolean;
	    errors: string[];

	    static createFrom(source: any = {}) {
	        return new SubscriptionSync(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.created = source["created"];
	        this.has_more = source["has_more"];
	        this.errors = source["errors"];
	    }
	}

}

export namespace desktopaccount {

	export class Status {
	    configured: boolean;
	    unlocked: boolean;
	    legacy_migration_required: boolean;
	    role: string;

	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.configured = source["configured"];
	        this.unlocked = source["unlocked"];
	        this.legacy_migration_required = source["legacy_migration_required"];
	        this.role = source["role"];
	    }
	}

}

export namespace main {

	export class DesktopVaultRecoveryResponse {
	    status: desktopaccount.Status;
	    recovery_code: string;

	    static createFrom(source: any = {}) {
	        return new DesktopVaultRecoveryResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = this.convertValues(source["status"], desktopaccount.Status);
	        this.recovery_code = source["recovery_code"];
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

export namespace models {

	export class BalanceHistoryResponse {
	    accounts: string[];
	    dates: string[];
	    balances: Record<string, Array<number>>;
	    balances_exact: Record<string, Array<string>>;

	    static createFrom(source: any = {}) {
	        return new BalanceHistoryResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accounts = source["accounts"];
	        this.dates = source["dates"];
	        this.balances = source["balances"];
	        this.balances_exact = source["balances_exact"];
	    }
	}
	export class LinkedTransactionResponse {
	    id: number;
	    fundItem: string;
	    date: string;
	    item: string;
	    type: string;
	    amount: number;
	    amount_exact: string;
	    memo: string;
	    linked_bank_ids?: number[];

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
	        this.amount_exact = source["amount_exact"];
	        this.memo = source["memo"];
	        this.linked_bank_ids = source["linked_bank_ids"];
	    }
	}
	export class CardReconciliationResponse {
	    applicable: boolean;
	    bank: LinkedTransactionResponse;
	    cards: LinkedTransactionResponse[];
	    card_total_exact: string;
	    bank_amount_exact: string;
	    difference_exact: string;
	    matched: boolean;
	    has_conflicts: boolean;

	    static createFrom(source: any = {}) {
	        return new CardReconciliationResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.applicable = source["applicable"];
	        this.bank = this.convertValues(source["bank"], LinkedTransactionResponse);
	        this.cards = this.convertValues(source["cards"], LinkedTransactionResponse);
	        this.card_total_exact = source["card_total_exact"];
	        this.bank_amount_exact = source["bank_amount_exact"];
	        this.difference_exact = source["difference_exact"];
	        this.matched = source["matched"];
	        this.has_conflicts = source["has_conflicts"];
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

	export class SubscriptionProvenance {
	    rule_id: string;
	    unconfirmed: boolean;
	    currency: string;
	    original_amount: string;
	    rate: string;
	    rate_date: string;
	    yen_amount_exact: string;

	    static createFrom(source: any = {}) {
	        return new SubscriptionProvenance(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rule_id = source["rule_id"];
	        this.unconfirmed = source["unconfirmed"];
	        this.currency = source["currency"];
	        this.original_amount = source["original_amount"];
	        this.rate = source["rate"];
	        this.rate_date = source["rate_date"];
	        this.yen_amount_exact = source["yen_amount_exact"];
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
	export class TagDeleteImpact {
	    tag_id: number;
	    tag_name: string;
	    descendant_count: number;
	    transaction_count: number;

	    static createFrom(source: any = {}) {
	        return new TagDeleteImpact(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tag_id = source["tag_id"];
	        this.tag_name = source["tag_name"];
	        this.descendant_count = source["descendant_count"];
	        this.transaction_count = source["transaction_count"];
	    }
	}
	export class TagSummary {
	    tag_id: number;
	    tag_name: string;
	    amount: number;
	    amount_exact: string;
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
	        this.amount_exact = source["amount_exact"];
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
	export class TransactionImageResponse {
	    id: number;
	    filename: string;
	    mime_type: string;
	    created_at: string;
	    data_url?: string;
	    invalid?: boolean;

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
	        this.invalid = source["invalid"];
	    }
	}
	export class TransactionImagePage {
	    images: TransactionImageResponse[];
	    next_cursor?: string;

	    static createFrom(source: any = {}) {
	        return new TransactionImagePage(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.images = this.convertValues(source["images"], TransactionImageResponse);
	        this.next_cursor = source["next_cursor"];
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

	export class TransactionRequest {
	    account: string;
	    date: string;
	    time: string;
	    item: string;
	    type: string;
	    amount: number;
	    memo: string;
	    images?: TransactionImageRequest[];
	    delete_image_ids?: number[];
	    new_tag_paths?: string[];
	    link_add_ids?: number[];
	    link_remove_ids?: number[];
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
	        this.delete_image_ids = source["delete_image_ids"];
	        this.new_tag_paths = source["new_tag_paths"];
	        this.link_add_ids = source["link_add_ids"];
	        this.link_remove_ids = source["link_remove_ids"];
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
	    subscription?: SubscriptionProvenance;
	    id: number;
	    fundItem: string;
	    account: string;
	    date: string;
	    item: string;
	    type: string;
	    amount: number;
	    amount_exact: string;
	    balance: number;
	    balance_exact: string;
	    memo: string;
	    images?: TransactionImageResponse[];
	    tags?: Tag[];

	    static createFrom(source: any = {}) {
	        return new TransactionResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.subscription = this.convertValues(source["subscription"], SubscriptionProvenance);
	        this.id = source["id"];
	        this.fundItem = source["fundItem"];
	        this.account = source["account"];
	        this.date = source["date"];
	        this.item = source["item"];
	        this.type = source["type"];
	        this.amount = source["amount"];
	        this.amount_exact = source["amount_exact"];
	        this.balance = source["balance"];
	        this.balance_exact = source["balance_exact"];
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
