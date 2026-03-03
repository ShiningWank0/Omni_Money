"""
Server Money - メインアプリケーション

個人や小規模組織向けの高機能Web家計簿アプリケーション

主な機能:
- セキュアな認証システム（bcryptハッシュ化、セッション管理、ログイン試行制限）
- 収入・支出の記録と管理
- 口座別の残高追跡
- 取引履歴の検索・編集・削除
- CSVファイルへのバックアップ
- 残高推移の可視化データ提供

技術スタック:
- Backend: Flask (Python)
- Database: SQLite with SQLAlchemy ORM
- Server: Waitress WSGI Server
- Authentication: bcrypt + python-dotenv
- Logging: Python標準ライブラリ（ファイルローテーション対応）

Author: ShiningWank0
Created: 2025
License: MIT
"""

from flask import Flask
from dotenv import load_dotenv
from waitress import serve

# .envファイルの読み込み
load_dotenv()

# 各モジュールのインポート
from config import init_config, setup_logging
from models import db
from utils import init_db
from auth import check_auth_setup
from routes.auth_routes import auth_bp
from routes.api_routes import api_bp
from routes.main_routes import main_bp

def create_app():
    """Flaskアプリケーションファクトリ
    
    Returns:
        Flask: 設定済みのFlaskアプリケーション
    """
    app = Flask(__name__)
    
    # 設定とログの初期化
    init_config(app)
    setup_logging(app)
    
    # 認証設定の確認
    if not check_auth_setup():
        exit(1)
    
    app.logger.info("認証設定を確認しました")
    
    # データベースの初期化
    db.init_app(app)
    
    # Blueprintの登録
    app.register_blueprint(auth_bp)
    app.register_blueprint(api_bp)
    app.register_blueprint(main_bp)
    
    app.logger.info("アプリケーションの初期化が完了しました")
    
    return app

def main():
    """メイン実行関数"""
    try:
        app = create_app()
        app.logger.info("アプリケーションを開始しています...")
        
        # データベースの初期化
        init_db(app)
        
        # ホストIPを設定から取得
        host_ip = app.config['HOST_IP']
        
        app.logger.info(f"サーバーを起動します (host={host_ip}, port=4000)")
        serve(app, host=host_ip, port=4000)
        
    except KeyboardInterrupt:
        if 'app' in locals():
            app.logger.info("アプリケーションが手動で停止されました")
    except Exception as e:
        if 'app' in locals():
            app.logger.error(f"アプリケーション実行中にエラーが発生しました: {e}", exc_info=True)
        else:
            print(f"アプリケーション起動エラー: {e}")
    finally:
        if 'app' in locals():
            app.logger.info("アプリケーションを終了します")

if __name__ == "__main__":
    main()