"""
Server Money - ユーティリティ関数

このファイルは、バックアップ管理、データベース初期化など
の共通機能を提供します。
"""

import csv
import io
import os
import glob
from datetime import datetime
from sqlalchemy import text, inspect
from models import db

def cleanup_old_backups(backup_dir, max_files=3):
    """バックアップディレクトリ内の古いCSVファイルを削除し、最新のmax_files件のみを保持する
    
    Args:
        backup_dir (str): バックアップディレクトリのパス
        max_files (int): 保持する最大ファイル数（デフォルト: 3）
    """
    from flask import current_app
    
    # バックアップファイルのパターン
    pattern = os.path.join(backup_dir, 'transactions_backup_*.csv')
    backup_files = glob.glob(pattern)
    
    if len(backup_files) <= max_files:
        return  # ファイル数が上限以下なら何もしない
    
    # ファイルを更新日時でソート（新しい順）
    backup_files.sort(key=os.path.getmtime, reverse=True)
    
    # 古いファイルを削除
    files_to_delete = backup_files[max_files:]
    for file_path in files_to_delete:
        try:
            os.remove(file_path)
            current_app.logger.info(f"古いバックアップファイルを削除しました: {file_path}")
        except OSError as e:
            current_app.logger.error(f"バックアップファイルの削除に失敗しました: {file_path}, エラー: {e}")

def init_db(app):
    """データベースを初期化する(SQLAlchemy 2.0対応)
    
    Args:
        app: Flaskアプリケーションインスタンス
    """
    try:
        with app.app_context():
            # テーブルの存在確認(SQLAlchemy 2.0対応)
            inspector = inspect(db.engine)
            tables = inspector.get_table_names()

            if 'transaction' not in tables:
                app.logger.info("テーブル 'transaction' が存在しないので作成します。")
                db.create_all()
                app.logger.info("データベースとテーブルが正常に作成されました。")
            else:
                app.logger.info("テーブル 'transaction' は既に存在します。")
    except Exception as e:
        app.logger.error(f"データベース初期化エラー: {e}")
        try:
            with app.app_context():
                db.create_all()
                app.logger.info("フォールバック: テーブルを作成しました。")
        except Exception as fallback_error:
            app.logger.error(f"フォールバック失敗: {fallback_error}")
            raise

def ensure_table_exists():
    """テーブルが存在しない場合に作成する(SQLAlchemy 2.0対応)"""
    from flask import current_app
    
    try:
        # テーブルの存在確認（簡単なクエリを実行）
        with db.engine.connect() as conn:
            conn.execute(text("SELECT 1 FROM transaction LIMIT 1"))
    except Exception:
        # テーブルが存在しない場合は作成
        current_app.logger.info("テーブルが存在しないため、作成します...")
        with current_app.app_context():
            db.create_all()
        current_app.logger.info("テーブルを作成しました")

def generate_unique_filename(directory, base_name, extension):
    """ユニークなファイル名を生成
    
    Args:
        directory (str): ディレクトリパス
        base_name (str): ベースファイル名
        extension (str): 拡張子
        
    Returns:
        str: ユニークなファイルパス
    """
    # タイムスタンプ付きファイル名（マイクロ秒も含めて重複を防ぐ）
    timestamp = datetime.now().strftime('%Y%m%d_%H%M%S_%f')
    filename = f'{directory}/{base_name}_{timestamp}.{extension}'
    
    # 既に同じファイル名が存在する場合は、追加のカウンターを付ける
    counter = 1
    original_filename = filename
    while os.path.exists(filename):
        base_part = original_filename.rsplit('.', 1)[0]
        filename = f'{base_part}_{counter}.{extension}'
        counter += 1
    
    return filename

def validate_transaction_data(data, required_fields=None):
    """取引データのバリデーション
    
    Args:
        data (dict): バリデーション対象のデータ
        required_fields (list): 必須フィールドのリスト
        
    Returns:
        tuple: (is_valid, error_message)
    """
    if required_fields is None:
        required_fields = ['account', 'date', 'item', 'type', 'amount']
    
    # 必須フィールドの検証
    for field in required_fields:
        if field not in data or not data[field]:
            return False, f'{field}は必須項目です'
    
    # タイプの検証
    if data.get('type') not in ['income', 'expense']:
        return False, 'typeは"income"または"expense"である必要があります'
    
    # 金額の検証
    try:
        amount = int(data['amount'])
        if amount <= 0:
            return False, '金額は正の数値である必要があります'
    except (ValueError, TypeError):
        return False, '金額は数値である必要があります'
    
    return True, None

def parse_transaction_date(date_str, time_str=None):
    """取引日時の解析
    
    Args:
        date_str (str): 日付文字列
        time_str (str, optional): 時刻文字列
        
    Returns:
        datetime: 解析された日時
        
    Raises:
        ValueError: 日付形式が正しくない場合
    """
    try:
        if time_str and time_str.strip():
            date_time_str = f"{date_str} {time_str}"
            return datetime.strptime(date_time_str, '%Y-%m-%d %H:%M')
        else:
            return datetime.strptime(date_str, '%Y-%m-%d')
    except ValueError:
        raise ValueError('日付形式が正しくありません')

def parse_csv_file(file_content):
    """CSVファイルの内容を解析してトランザクションデータに変換
    
    Args:
        file_content (str): CSVファイルの内容
        
    Returns:
        tuple: (success, result, error_message)
            success: 成功時True、失敗時False
            result: 成功時は解析されたデータのリスト、失敗時はNone
            error_message: 失敗時のエラーメッセージ
    """
    try:
        # CSVファイルを読み取り
        csv_reader = csv.DictReader(io.StringIO(file_content))
        
        # 必要なヘッダーの確認
        required_headers = ['account', 'date', 'item', 'type', 'amount']
        if not all(header in csv_reader.fieldnames for header in required_headers):
            missing_headers = [h for h in required_headers if h not in csv_reader.fieldnames]
            return False, None, f'必須のヘッダーが不足しています: {", ".join(missing_headers)}'
        
        transactions = []
        row_number = 1  # データ行の行番号（ヘッダー除く）
        
        for row in csv_reader:
            row_number += 1
            
            # 各行のバリデーション
            is_valid, error_msg = validate_csv_row(row, row_number)
            if not is_valid:
                return False, None, error_msg
            
            # データを適切な形式に変換
            try:
                # 日付の解析
                date_str = row['date'].strip()
                if ' ' in date_str:
                    # 日時形式の場合
                    date_obj = datetime.strptime(date_str, '%Y-%m-%d %H:%M:%S')
                else:
                    # 日付のみの場合
                    date_obj = datetime.strptime(date_str, '%Y-%m-%d')
                
                transaction_data = {
                    'account': row['account'].strip(),
                    'date': date_obj,
                    'item': row['item'].strip(),
                    'type': row['type'].strip().lower(),
                    'amount': int(row['amount']),
                    'balance': int(row.get('balance', 0)) if row.get('balance', '').strip() else None
                }
                
                transactions.append(transaction_data)
                
            except (ValueError, TypeError) as e:
                return False, None, f'行 {row_number}: データ形式エラー - {str(e)}'
        
        return True, transactions, None
        
    except Exception as e:
        return False, None, f'CSVファイルの読み取りに失敗しました: {str(e)}'

def validate_csv_row(row, row_number):
    """CSVの1行をバリデーション
    
    Args:
        row (dict): CSV行データ
        row_number (int): 行番号
        
    Returns:
        tuple: (is_valid, error_message)
    """
    # 必須フィールドの検証
    required_fields = ['account', 'date', 'item', 'type', 'amount']
    for field in required_fields:
        if not row.get(field, '').strip():
            return False, f'行 {row_number}: {field}が空です'
    
    # タイプの検証
    transaction_type = row['type'].strip().lower()
    if transaction_type not in ['income', 'expense']:
        return False, f'行 {row_number}: typeは"income"または"expense"である必要があります（現在の値: {row["type"]}）'
    
    # 金額の検証
    try:
        amount = float(row['amount'])
        if amount <= 0:
            return False, f'行 {row_number}: 金額は正の数値である必要があります（現在の値: {row["amount"]}）'
    except (ValueError, TypeError):
        return False, f'行 {row_number}: 金額は数値である必要があります（現在の値: {row["amount"]}）'
    
    # 日付の検証
    date_str = row['date'].strip()
    try:
        if ' ' in date_str:
            # 日時形式
            datetime.strptime(date_str, '%Y-%m-%d %H:%M:%S')
        else:
            # 日付のみ
            datetime.strptime(date_str, '%Y-%m-%d')
    except ValueError:
        return False, f'行 {row_number}: 日付形式が正しくありません（現在の値: {row["date"]}）。YYYY-MM-DD または YYYY-MM-DD HH:MM:SS 形式で入力してください'
    
    # 残高の検証（オプション）
    if row.get('balance', '').strip():
        try:
            float(row['balance'])
        except (ValueError, TypeError):
            return False, f'行 {row_number}: 残高は数値である必要があります（現在の値: {row["balance"]}）'
    
    return True, None

def import_csv_transactions(transactions_data, overwrite_mode='append'):
    """CSVから解析したトランザクションデータをデータベースにインポート
    
    Args:
        transactions_data (list): 解析済みのトランザクションデータ
        overwrite_mode (str): インポートモード ('append' または 'replace')
        
    Returns:
        tuple: (success, imported_count, error_message)
    """
    from models import Transaction
    from flask import current_app
    
    try:
        # replaceモードの場合、既存データを全削除
        if overwrite_mode == 'replace':
            current_app.logger.info("replaceモードでCSVインポート - 既存データを削除中")
            Transaction.query.delete()
            db.session.commit()
        
        imported_count = 0
        
        for transaction_data in transactions_data:
            # 新しいトランザクションを作成（balanceは一時的に0で作成）
            transaction = Transaction(
                account=transaction_data['account'],
                date=transaction_data['date'],
                item=transaction_data['item'],
                type=transaction_data['type'],
                amount=transaction_data['amount'],
                balance=0  # 後で再計算する
            )
            
            db.session.add(transaction)
            imported_count += 1
        
        # データベースにコミット
        db.session.commit()
        current_app.logger.info(f"CSVインポート完了: {imported_count}件のトランザクションを追加")
        
        # 全ての口座の残高を再計算
        accounts = db.session.query(Transaction.account.distinct()).all()
        for account_tuple in accounts:
            account = account_tuple[0]
            _recalculate_balance_for_account_util(account)
        
        current_app.logger.info("全口座の残高再計算完了")
        
        return True, imported_count, None
        
    except Exception as e:
        db.session.rollback()
        current_app.logger.error(f"CSVインポートでエラーが発生しました: {str(e)}", exc_info=True)
        return False, 0, f'インポート中にエラーが発生しました: {str(e)}'

def _recalculate_balance_for_account_util(account):
    """指定口座の残高を再計算するユーティリティ関数
    
    Args:
        account (str): 口座名
    """
    from models import Transaction
    
    txs = Transaction.query.filter_by(account=account).order_by(Transaction.date, Transaction.id).all()
    running_balance = 0
    for tx in txs:
        if tx.type == 'income':
            running_balance += tx.amount
        else:
            running_balance -= tx.amount
        tx.balance = running_balance
    db.session.commit()