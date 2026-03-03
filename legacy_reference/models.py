"""
Server Money - データベースモデル定義

このファイルは、SQLAlchemyを使用したデータベースモデルの定義を含みます。
"""

from flask_sqlalchemy import SQLAlchemy
from datetime import datetime

db = SQLAlchemy()

class Transaction(db.Model):
    """取引データモデル
    
    Attributes:
        id: 主キー
        account: 資金項目（口座名）
        date: 取引日時
        item: 取引項目名
        type: 取引種別（'income' or 'expense'）
        amount: 金額
        balance: 残高（自動計算）
    """
    
    id = db.Column(db.Integer, primary_key=True)
    account = db.Column(db.String(100), nullable=False)
    date = db.Column(db.DateTime, nullable=False)
    item = db.Column(db.String(200), nullable=False)
    type = db.Column(db.String(10), nullable=False)  # 'income' or 'expense'
    amount = db.Column(db.Integer, nullable=False)
    balance = db.Column(db.Integer, nullable=False)
    
    def to_dict(self):
        """辞書形式でデータを返す（JSON化用）
        
        Returns:
            dict: 取引データの辞書
        """
        return {
            'id': self.id,
            'fundItem': self.account,  # フロントエンド用にfundItemとして返す
            'account': self.account,   # 後方互換性のため残す
            'date': self.date.strftime('%Y-%m-%d %H:%M:%S') if self.date.time() != datetime.min.time() else self.date.strftime('%Y-%m-%d'),
            'item': self.item,
            'type': self.type,
            'amount': self.amount,
            'balance': self.balance
        }

    def __repr__(self):
        """デバッグ用の文字列表現"""
        return f'<Transaction {self.id}: {self.account} - {self.item} - {self.amount}円>'